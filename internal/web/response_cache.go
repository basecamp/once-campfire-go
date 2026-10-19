package web

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/front"
)

// The whole-response cache (upstream e3a1309, raised from budget zero): the
// completed HTML responses of the room and messages-page routes, keyed by the
// observed database generation (PRAGMA data_version on a pinned reader — the
// same namespace fragmentKey uses), the authenticated user and every request
// input that can change the bytes. A warm request re-checks the session and
// the generation, then serves the stored wire body: no message reads, no
// template execution, no per-request compression or assembly, no validator
// computation. This is the shape of the C reference's "64 MiB
// response-body cache" hits (authorization re-checked, external SQLite
// commits observed), which is what wins its warm reads.
//
// The store copies the exact wire bytes the fresh render produced (the
// assembled gzip member for gzip clients, the joined pieces for identity
// clients), so a hit is byte-identical to a fresh render by construction; the
// stored entity headers (Content-Type, ETag, Last-Modified, Cache-Control,
// Vary, Content-Encoding) are the same ones the fresh response emits. A
// session flash is per-request state and never cached. The finer-grained
// engine caches (pieces, refs, search) still serve the miss path under
// writes; a committed write moves the generation and both the old entries and
// the next fill age out exactly as the sidebar gate does (single-process
// observation limit, README.md). The entry cost gate and LRU are the same
// shape as the fragment cache's. The sidebar is deliberately absent (see
// beginResponseCache): its own whole-page cache already serves the warm shape.
// Search takes part in the round; its own result cache still serves the miss
// path, and the response key covers the query and the generation so a purge or
// a write moves the served bytes.

type cachedResponse struct {
	digest  [32]byte
	key     []byte
	version uint64
	body    []byte
	header  http.Header
	cost    int
	// explicitLength records whether the fresh response set Content-Length
	// itself (the recorded and precomposed paths) rather than leaving framing
	// to the server (the map path). A hit reproduces the same choice so the
	// wire framing is identical to the fresh response.
	explicitLength bool
	// head is the precomposed replay metadata: the exact header lines the
	// fresh 200 emitted — the recorded head block verbatim when the fresh
	// response was framed, the live map's sorted lines otherwise — with
	// Content-Length and the front's X-Cache marker included. It is immutable
	// and right-sized (cap == len), so no seam can splice into a shared entry.
	// nil when the entry must take the map path (a body the server framed
	// itself, or a map carrying a receiver-owned line).
	head []byte
	// head304 is the precomposed 304 replay block for the recorded flavor (the
	// no-cache block with X-Cache baked in); nil for the validator flavor,
	// whose hit takes the map path (see serveCached304).
	head304 []byte
	// xcache is the front's X-Cache marker baked into head and head304. A hit
	// whose live marker differs takes the map path, because the marker is a
	// property of the request (the front's cache eligibility) and not of the
	// stored bytes.
	xcache string
	// validator304 records the map-rendered flavor of conditional responses,
	// whose fresh 304 carries validator headers instead of the recorded block.
	validator304 bool
	// etag and lastModified are the stored validator, captured once at
	// admission so a hit's conditional check needs no string->[]byte or
	// http.ParseTime allocation.
	etag         []byte
	lastModified time.Time
	// encodingValues is the single-element value slice for the stored wire
	// content coding, nil for identity. A replay puts it on the live map (see
	// setMapEncoding). It is shared read-only: len == cap == 1, so a Header.Add
	// on the key allocates instead of writing into the entry.
	encodingValues  []string
	encodingStorage [1]string
}
type responseCache struct {
	mu           sync.Mutex
	entries      map[[32]byte]*list.Element
	order        list.List
	limit, size  int
	version      uint64
	hits, misses uint64
}
type responseRound struct {
	version          uint64
	user             int64
	keyed            bool
	gzip, hit, flash bool
}

func newResponseCache(limit int) *responseCache {
	return &responseCache{limit: limit, entries: make(map[[32]byte]*list.Element)}
}

// get looks a completed response up by the digest of its exact key bytes. A
// digest hit is confirmed against the stored key bytes, so a hash collision
// reads as a miss rather than another request's page.
func (c *responseCache) get(digest [32]byte, key []byte, version uint64) *cachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if version < c.version {
		c.misses++
		return nil
	}
	if version != c.version {
		c.entries = make(map[[32]byte]*list.Element)
		c.order.Init()
		c.size = 0
		c.version = version
	}
	element := c.entries[digest]
	if element == nil {
		c.misses++
		return nil
	}
	entry := element.Value.(*cachedResponse)
	if !bytes.Equal(entry.key, key) {
		c.misses++
		return nil
	}
	c.order.MoveToFront(element)
	c.hits++
	return entry
}
func (c *responseCache) put(entry *cachedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.version != c.version || len(entry.key) > 8192 || entry.cost > c.limit/8 {
		return
	}
	if c.entries[entry.digest] != nil {
		return
	}
	for c.size+entry.cost > c.limit && c.order.Len() > 0 {
		oldest := c.order.Back()
		old := oldest.Value.(*cachedResponse)
		delete(c.entries, old.digest)
		c.order.Remove(oldest)
		c.size -= old.cost
	}
	c.entries[entry.digest] = c.order.PushFront(entry)
	c.size += entry.cost
}
func (c *responseCache) counters() (hits, misses uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// stats reports the LRU occupancy so tests and benchmarks can assert that an
// eviction actually happened rather than assuming it.
func (c *responseCache) stats() (entries, size, limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.size, c.limit
}

// beginResponseCache starts a caching round for a request when the route is
// one the cache covers and the encoding is one it can store. It runs after
// the browser session is attached (for the flash check) and before any read.
// The generation is the one ServeHTTP observed moments earlier (the same
// value responseHit re-checks against a fresh read, so a commit between the
// two observations misses the cache rather than serving stale bytes); it
// adds no SQLite read of its own. The sidebar is deliberately absent: its
// own whole-page cache already serves warm requests, and a round here would
// only add the generation read.
func (s *Server) beginResponseCache(r *http.Request) {
	info := requestMetadata(r.Context())
	if info == nil {
		return
	}
	if s.responses.limit == 0 {
		// Budget zero disables storage; every lookup would miss, so the
		// round (and its per-request work) is skipped entirely — the rollback
		// switch costs nothing and every request behaves exactly as before
		// the cache existed.
		return
	}
	if (r.Method != "GET" && r.Method != "HEAD") || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return
	}
	route, err := recognizeContract(r.Method, r.URL.EscapedPath())
	if err != nil || route == nil {
		return
	}
	switch route.Endpoint {
	// The sidebar is absent: its own whole-page cache already serves warm
	// requests. Search takes part: the round stores the search page's wire
	// bytes, and the key covers the query and the observed generation, so a
	// POST/DELETE that purges the result cache (and commits) moves the
	// generation and the next GET misses. The result cache still serves the
	// miss path.
	case "rooms#show", "messages#index", "searches#index":
	default:
		return
	}
	encoding := front.ResponseEncoding(r.Header.Get("Accept-Encoding"))
	if encoding == "" {
		return
	}
	state := browserState(r)
	state.load()
	info.response = &responseRound{
		version: info.databaseVersion,
		gzip:    encoding == "gzip",
		flash:   state.values["flash"] != nil,
	}
}

// responseHit looks the completed response up. The key covers everything the
// rendered bytes can depend on: the observed generation, the authenticated
// user, the request URI and form, the identity headers (cookie, accept,
// turbo-frame, user agent, origin, X-Requested-With), the negotiated encoding
// and the process revision. Auth has already re-checked the session in the
// auth middleware; a fresh generation read here observes any commit since the
// round began.
func (s *Server) responseHit(r *http.Request) *cachedResponse {
	info := requestMetadata(r.Context())
	if info == nil || info.response == nil {
		return nil
	}
	round := info.response
	if round.flash || round.user == 0 {
		return nil
	}
	if !s.currentRequestGeneration(r, round.version) {
		return nil
	}
	version := round.version
	// The key is built into a stack buffer and hashed in place: the map key is
	// the digest, and a hit is confirmed against the entry's stored key bytes.
	// A lookup therefore allocates nothing; only an admission copies the key.
	// A shape outside the zero-allocation envelope (a key longer than the
	// stack buffer, or more form keys than the stack array holds) is bypassed
	// here, before the builder can grow a slice; round.keyed stays false so the
	// store is skipped too.
	if _, ok := responseKeySize(info, r, round, version); !ok {
		return nil
	}
	var buf [responseKeyBufSize]byte
	key := appendResponseCacheKey(buf[:0], info, r, round, version)
	round.keyed = true
	entry := s.responses.get(sha256.Sum256(key), key, version)
	round.hit = entry != nil
	return entry
}

// responseKeyBufSize is the stack buffer the whole-response key is built into
// and maxResponseFormKeys is the stack array that holds the sorted form keys.
// Both bound the zero-allocation envelope: a request whose key would exceed
// either is rejected by responseKeySize before the builder runs, so no append
// grows a slice and no form key spills to the heap. The cache paths consult
// the preflight; appendResponseCacheKey itself stays correct for any input
// (its overflow branch allocates) so a future caller that skips the preflight
// still gets an injective key.
const (
	responseKeyBufSize  = 1536
	maxResponseFormKeys = 16
)

// responseKeySize computes the exact byte length appendResponseCacheKey would
// append, without building the key, and reports whether that length fits the
// stack buffer and the form-key cap. It mirrors the builder's length-prefixed
// encoding; TestResponseCacheKeyEnvelope pins the two against each other.
func responseKeySize(info *requestInfo, r *http.Request, round *responseRound, version uint64) (int, bool) {
	if len(r.Form) > maxResponseFormKeys {
		return 0, false
	}
	var num [20]byte
	digits := func(v int64) int { return len(strconv.AppendInt(num[:0], v, 10)) }
	udigits := func(v uint64) int { return len(strconv.AppendUint(num[:0], v, 10)) }
	field := func(value string) int { return digits(int64(len(value))) + len(value) + 2 }
	size := field(info.host) + field(info.origin) + field(r.RequestURI)
	if r.RequestURI == "" {
		size += field(r.URL.EscapedPath()) + field(r.URL.RawQuery)
	}
	for key, values := range r.Form {
		for _, value := range values {
			size += 1 + field(key) + field(value)
		}
	}
	size += digits(round.user) + 1
	for _, header := range [3]string{"Cookie", "Accept", "Turbo-Frame"} {
		values := r.Header.Values(header)
		size += digits(int64(len(values))) + 1
		for _, value := range values {
			size += field(value)
		}
	}
	size += field(r.UserAgent()) + field(r.Header.Get("Origin")) + field(r.Header.Get("X-Requested-With"))
	if round.gzip {
		size += len("gzip|")
	} else {
		size += len("idn|")
	}
	size += field(os.Getenv("GIT_REVISION"))
	size += udigits(version)
	return size, size <= responseKeyBufSize
}

// appendResponseCacheKey appends the length-prefixed cache key to b and
// returns it: every input is length-prefixed, so the key cannot alias
// different inputs. It replaces the original json.Marshal (which spent ~5% of
// a warm room route's samples) and then the string-returning builder (which
// allocated once per request); the caller hashes the returned bytes into a
// [32]byte map key. Building and hashing the key allocates nothing for the
// shapes responseKeySize admits; the hit path separately allocates the header
// value slices it copies onto the live writer (serveCached), which is not part
// of this envelope.
func appendResponseCacheKey(b []byte, info *requestInfo, r *http.Request, round *responseRound, version uint64) []byte {
	field := func(value string) {
		b = strconv.AppendInt(b, int64(len(value)), 10)
		b = append(b, ':')
		b = append(b, value...)
		b = append(b, '|')
	}
	field(info.host)
	field(info.origin)
	field(r.RequestURI)
	if r.RequestURI == "" {
		// A server request always carries the raw request-target, and the
		// parsed URL is derived from it, so the normalized reconstruction is
		// redundant then. When it is absent (a synthesized request), cover the
		// parsed parts instead. r.URL.RequestURI() would append the same bytes
		// but allocates a string per call.
		field(r.URL.EscapedPath())
		field(r.URL.RawQuery)
	}
	if len(r.Form) > 0 {
		// The form is written in sorted key order so the key is independent of
		// map iteration order. The keys are sorted in a stack array. The cache
		// paths cap the form at maxResponseFormKeys distinct keys
		// (responseKeySize), so the stack array always holds them and no heap
		// key slice is built; sort.Strings would force the stack array through
		// an interface and escape, so the insertion sort is inline. The
		// overflow branch below is a correctness fallback for a caller that
		// skips the preflight — it allocates.
		var fixed [maxResponseFormKeys]string
		n := 0
		overflow := false
		for key := range r.Form {
			if n == len(fixed) {
				overflow = true
				break
			}
			i := n
			for i > 0 && fixed[i-1] > key {
				fixed[i] = fixed[i-1]
				i--
			}
			fixed[i] = key
			n++
		}
		if overflow {
			keys := make([]string, 0, len(r.Form))
			for key := range r.Form {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				for _, value := range r.Form[key] {
					b = append(b, '&')
					field(key)
					field(value)
				}
			}
		} else {
			for i := 0; i < n; i++ {
				key := fixed[i]
				for _, value := range r.Form[key] {
					b = append(b, '&')
					field(key)
					field(value)
				}
			}
		}
	}
	b = strconv.AppendInt(b, round.user, 10)
	b = append(b, '|')
	for _, header := range [3]string{"Cookie", "Accept", "Turbo-Frame"} {
		values := r.Header.Values(header)
		b = strconv.AppendInt(b, int64(len(values)), 10)
		b = append(b, ':')
		for _, value := range values {
			field(value)
		}
	}
	field(r.UserAgent())
	field(r.Header.Get("Origin"))
	field(r.Header.Get("X-Requested-With"))
	if round.gzip {
		b = append(b, "gzip|"...)
	} else {
		b = append(b, "idn|"...)
	}
	field(os.Getenv("GIT_REVISION"))
	b = strconv.AppendUint(b, version, 10)
	return b
}

// serveCached writes a cache hit. A conditionally fresh request is answered
// first with the 304 the fresh path produced (see serveCached304). Otherwise
// the hit takes the direct-replay lane when the entry carries an immutable
// head block (replayCached); failing that it restores the stored entity
// headers onto the live writer and serves the stored wire body through the
// response buffer's parts channel — no body copy; finish computes
// Content-Length from the part and answers conditional requests against the
// stored validator exactly like the map path. Restoring the header map copies
// each stored value slice, so that fallback allocates once per header value.
// A framed request skipped the fixed security headers in ServeHTTP; they are
// restored here so the hit head is byte-identical to the fresh response's.
func (s *Server) serveCached(entry *cachedResponse, w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	// The conditional check mirrors finish's notModified exactly (same
	// If-None-Match token comparison, same If-Modified-Since branch); the
	// stored validator was parsed at admission, so this costs no allocation.
	if requestIsFresh(r, entry.etag, entry.lastModified) {
		s.serveCached304(entry, w, r, h)
		return
	}
	if s.replayCached(entry, w, r, h) {
		return
	}
	for key, values := range entry.header {
		h[key] = append([]string(nil), values...)
	}
	if buffered := findResponseBuffer(w); buffered != nil {
		if buffered.framed {
			s.restoreRecordedHeaders(h)
		}
		buffered.parts = buffered.partsBuf[:1]
		buffered.parts[0] = entry.body
		// A map-path stored response left Content-Length to the server; the
		// hit must do the same (see cachedMapBody).
		buffered.cachedMapBody = !entry.explicitLength
		return
	}
	// No response buffer in the chain (direct capture writers): write the
	// body through. Headers and status were set above.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.body)
}

// replayCached emits a 200/HEAD hit through the precomposed seam when the
// entry carries a replay head block. Every gate the fresh precomposed path
// applies is applied here, in the same order: the lane's own flag, the live
// X-Cache marker the baked block was rendered with, the chain's precomposed
// receiver, the response policy's pass-through answer for the stored wire
// coding, and the request-side gates (no Set-Cookie, unchanged session). The
// entry's head block is right-sized and carries the spliced X-Cache line, so
// the seam performs no copy: the shared entry is never written to. It reports
// whether the hit was emitted.
func (s *Server) replayCached(entry *cachedResponse, w http.ResponseWriter, r *http.Request, h http.Header) bool {
	if !s.precomposed || entry.head == nil {
		return false
	}
	if h.Get("X-Cache") != entry.xcache {
		// The front's marker is a request property (its own cache eligibility)
		// that can differ between the fill and the hit; the baked block would
		// then emit the wrong value, so this hit takes the map path.
		return false
	}
	buffered := findResponseBuffer(w)
	if buffered == nil || findPrecomposedReceiver(w) == nil {
		return false
	}
	if !precomposedFramingAllowed(w, entry.header.Get("Content-Encoding")) {
		return false
	}
	if !precomposedEligible(r, h, browserStateIfAny(r)) {
		return false
	}
	buffered.recordedHead = entry.head
	buffered.recordedStatus = http.StatusOK
	buffered.precomposed = true
	buffered.parts = buffered.partsBuf[:1]
	buffered.parts[0] = entry.body
	entry.setMapEncoding(h)
	// Mark the response as written exactly as the fresh framed path does: the
	// session was checked unchanged, so its commit is a no-op, and the
	// deferred session write must not emit a second head.
	if sw, ok := w.(*sessionWriter); ok {
		sw.written = true
	} else {
		buffered.status = http.StatusOK
	}
	s.responseReplays.Add(1)
	return true
}

// setMapEncoding puts the stored wire coding on the live map, exactly as the
// fresh framed path does (recorded_pieces.go's Content-Encoding line).
//
// The Deflate middleware inside the chain decides pass-through from the header
// map, and the precomposed lane bypasses its WriteHeader, so the map is the
// only place it can learn the body is already encoded. Without the coding it
// starts its own gzip writer after the response is already on the wire; at
// close that writer emits a lone gzip member, which the owned loop's
// Content-Length guard rejects and which marks the connection broken — the
// keep-alive connection is then closed without a Connection: close header, so
// the client's next request on it fails.
//
// The slice is the entry's own, shared read-only (len == cap == 1, so an Add
// on the key allocates rather than writing into the entry).
func (e *cachedResponse) setMapEncoding(h http.Header) {
	if e.encodingValues != nil {
		h["Content-Encoding"] = e.encodingValues
	}
}

// serveCached304 writes a cache hit's 304. The fresh 304 is not the 200 head
// with a different status: recorded responses answer from finish's
// notModified with a "no-cache" fallback, while map-rendered responses carry
// the validator's cache-control and ETag.
// The entry records which flavor it was and the hit reproduces it.
//
// The recorded flavor's block carries the response's coding, so it replays
// through the same lane as the 200 — with the coding on the live map, for the
// reason setMapEncoding documents. The validator flavor's fresh 304 carries no
// coding, so its map must not either; with no coding there an inner middleware
// would start its own compressor after the 304 is on the wire, so that flavor
// takes the map path instead, whose 304 the middleware does see (a 304 status
// is its pass-through case) and whose bytes are identical to the fresh
// validator 304.
func (s *Server) serveCached304(entry *cachedResponse, w http.ResponseWriter, r *http.Request, h http.Header) {
	buffered := findResponseBuffer(w)
	if buffered == nil {
		entry.apply304Map(h)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// The precomposed 304 lane mirrors the fresh framed branch, which only
	// runs for a non-HEAD request (a HEAD 304 is composed on the map path).
	if !entry.validator304 && s.precomposed && r.Method != "HEAD" && h.Get("X-Cache") == entry.xcache && findPrecomposedReceiver(w) != nil {
		if precomposedFramingAllowed(w, entry.header.Get("Content-Encoding")) && precomposedEligible(r, h, browserStateIfAny(r)) {
			entry.setMapEncoding(h)
			buffered.recordedHead = entry.head304
			buffered.recordedStatus = http.StatusNotModified
			buffered.precomposed = true
			// Same as the 200 lane: the session was checked unchanged, so the
			// deferred session write must not run.
			if sw, ok := w.(*sessionWriter); ok {
				sw.written = true
			} else {
				buffered.status = http.StatusNotModified
			}
			s.responseReplays.Add(1)
			return
		}
	}
	entry.apply304Map(h)
	buffered.status = http.StatusNotModified
}

// apply304Map sets the exact map lines this entry's fresh 304 carried, so the
// map path composes the identical head: the selected ETag plus the recorded
// flavor's no-cache fallback and content coding, or the validator flavor's
// cache-control.
func (e *cachedResponse) apply304Map(h http.Header) {
	h.Set("ETag", e.header.Get("ETag"))
	if e.validator304 {
		h.Set("Cache-Control", e.header.Get("Cache-Control"))
		return
	}
	h.Set("Cache-Control", "no-cache")
	if encoding := e.header.Get("Content-Encoding"); encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
}

// cacheResponse stores the completed response in the whole-response cache.
// It runs in finish before emission, so the store sees exactly the wire body
// and the final header state the request is about to receive — on the framed
// path (where the entity headers live in the precomposed head block) and the
// map path alike. Only completed, non-exceptional, cacheable GET responses
// enter; everything else (flash renders, conditional 304s, failures, zstd
// bodies, non-HTML content) falls through untouched.
func (s *Server) cacheResponse(r *http.Request, w *responseBuffer) {
	info := requestMetadata(r.Context())
	if info == nil || info.response == nil {
		return
	}
	if info.generationChecked && !info.generationCurrent {
		return
	}
	round := info.response
	if round.hit || round.flash || !round.keyed || r.Method != "GET" || w.status != 200 || w.exception {
		return
	}
	if w.wireEncoding != "" && w.wireEncoding != "gzip" || w.wireEncoding == "gzip" != round.gzip {
		// A zstd-coded response is never stored; the stored coding must be
		// the one the key names.
		return
	}
	h := w.Header()
	if h.Get("Content-Type") == "" {
		// The framed path keeps the entity headers out of the map; a framed
		// response is a recorded page, so the constants apply.
		if w.recordedStatus != 0 && w.recordedStatus != http.StatusOK {
			return
		}
	} else {
		if !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			return
		}
		if control := h.Get("Cache-Control"); strings.Contains(control, "no-store") || strings.Contains(control, "no-transform") {
			return
		}
	}
	wire := w.encoded
	total := len(wire)
	if wire == nil && len(w.parts) > 0 {
		for _, part := range w.parts {
			total += len(part)
		}
	} else if wire == nil {
		wire = w.body.Bytes()
		total = len(wire)
	}
	// The key is rebuilt into a stack buffer (the same bytes responseHit
	// hashed) so the entry can keep the exact key without a per-request copy;
	// only the admission clones it. round.keyed is only set when responseHit's
	// preflight admitted the shape, so the builder cannot grow the buffer here;
	// the preflight is repeated for a caller that reaches cacheResponse without
	// responseHit.
	if _, ok := responseKeySize(info, r, round, round.version); !ok {
		return
	}
	var keyBuf [responseKeyBufSize]byte
	key := appendResponseCacheKey(keyBuf[:0], info, r, round, round.version)
	if total == 0 || total+len(key)+1024 > s.responses.limit/8 {
		return
	}
	// Parts are borrowed, but their joined buffer is newly owned by this
	// entry. Reject oversized bodies before allocating it; clone only borrowed
	// contiguous buffers. The final generation check still gates publication.
	if wire == nil {
		wire = make([]byte, 0, total)
		for _, part := range w.parts {
			wire = append(wire, part...)
		}
	} else {
		wire = bytes.Clone(wire)
	}
	header := make(http.Header)
	if h.Get("Content-Type") == "" {
		// Framed: reconstruct the entity headers from the head block's
		// inputs, in the same values the map path emits.
		header.Set("Content-Type", "text/html; charset=utf-8")
		header.Set("Cache-Control", "max-age=0, private, must-revalidate")
		header.Set("Vary", "Accept-Encoding")
		if w.recordedEtag != nil {
			header.Set("ETag", string(append([]byte(nil), w.recordedEtag...)))
		}
		if modified := h.Get("Last-Modified"); modified != "" {
			header.Set("Last-Modified", modified)
		}
		if w.wireEncoding != "" {
			header.Set("Content-Encoding", w.wireEncoding)
		}
	} else {
		for _, name := range []string{"Content-Type", "ETag", "Last-Modified", "Cache-Control", "Link", "Vary", "Content-Encoding"} {
			if values := h.Values(name); len(values) > 0 {
				header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
			}
		}
	}
	if !strings.Contains(header.Get("Vary"), "Accept-Encoding") {
		header.Add("Vary", "Accept-Encoding")
	}
	entry := &cachedResponse{digest: sha256.Sum256(key), key: bytes.Clone(key), version: round.version, body: wire, header: header, explicitLength: len(w.encoded) > 0 || w.encodedLength > 0 || len(w.parts) > 0}
	// The replay metadata: the exact head block the fresh response emitted,
	// rendered once here so a hit does no map, sort or sanitize work and no
	// header-value copy. It is only built for the responses whose framing is
	// fully decided by an explicit Content-Length (the recorded/encoded
	// paths); a body the server framed itself (chunked or inferred length)
	// keeps the map path, which is the baseline shape the framing tests pin.
	entry.validator304 = !entry.explicitLength
	entry.etag = append([]byte(nil), header.Get("ETag")...)
	if modified := header.Get("Last-Modified"); modified != "" {
		if parsed, err := http.ParseTime(modified); err == nil {
			entry.lastModified = parsed
		}
	}
	entry.xcache = h.Get("X-Cache")
	if encoding := header.Get("Content-Encoding"); encoding != "" {
		entry.encodingStorage[0] = encoding
		entry.encodingValues = entry.encodingStorage[:]
	}
	entry.head304 = s.buildReplay304(header, entry.validator304, entry.xcache)
	if entry.explicitLength {
		entry.head = s.buildReplayHead(h, framedHead(w), len(wire), entry.xcache)
	}
	entry.cost = cap(entry.body) + len(entry.key) + 1024 + len(entry.head) + len(entry.head304)
	for name, values := range header {
		entry.cost += len(name)
		for _, value := range values {
			entry.cost += len(value)
		}
	}
	// The single fresh generation read of the admission window: a commit since
	// the pre-read round must not be admitted under the old generation. The
	// hit path's own fresh read rejects a commit between the round and the hit;
	// this one rejects a commit that landed during the render. The two reads
	// the store used to take (one before the wire assembly, one here) were
	// redundant — both compared against the same pre-read generation — so the
	// store now takes exactly one, as late as possible before the put.
	version, err := s.DB.ResponseVersion(r.Context())
	if err == nil && version == round.version {
		s.responses.put(entry)
	}
}

func responseCacheBudget() (int, error) {
	raw, ok := os.LookupEnv("CAMPFIRE_RESPONSE_CACHE_MB")
	if !ok || raw == "" {
		return 64 << 20, nil
	}
	mb, err := strconv.Atoi(raw)
	if err != nil || mb < 0 || mb > 1024 {
		return 0, strconv.ErrSyntax
	}
	return mb << 20, nil
}
