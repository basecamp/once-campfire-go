package front

import (
	"bytes"
	"container/list"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	key     string
	header  http.Header
	body    []byte
	status  int
	variant map[string]string
	expires time.Time
	size    int64
	// recorded is the precomposed replay lane's state (ENGINE-62); nil when
	// the entry must be served through the header-map path.
	recorded *recordedResponse
}
type Cache struct {
	mu                      sync.Mutex
	entries                 map[string]*list.Element
	order                   *list.List
	size, capacity, maxItem int64
	// FixedRoutes enables the fixed-route table (fixed.go); see
	// Config.FixedRoutes. The zero value disables it, so NewCache's default
	// is on (matching the production FromEnv default).
	FixedRoutes bool
	// fixed holds the precomputed responses of the fixed routes: the first
	// unconditional GET captures the route's application response once per
	// content encoding and every later request replays the captured bytes
	// without invoking the app. Bounded by construction: one pair for each
	// path in fixedRoutes.
	fixed       map[string]*fixedPair
	fixedFailed bool
}

func NewCache(capacity, maxItem int64) *Cache {
	return &Cache{entries: map[string]*list.Element{}, order: list.New(), capacity: capacity, maxItem: maxItem, FixedRoutes: true, fixed: map[string]*fixedPair{}}
}

var publicDirective = regexp.MustCompile(`\bpublic\b`)
var noCacheDirective = regexp.MustCompile(`\bno-cache\b`)
var sharedMaxAge = regexp.MustCompile(`\bs-max-age=(\d+)\b`)
var maxAge = regexp.MustCompile(`\bmax-age=(\d+)\b`)

func lifetime(status int, h http.Header) time.Duration {
	if status < 200 || status > 399 || status == 304 || strings.Contains(h.Get("Vary"), "*") {
		return 0
	}
	cc := h.Get("Cache-Control")
	if !publicDirective.MatchString(cc) || noCacheDirective.MatchString(cc) {
		return 0
	}
	match := sharedMaxAge.FindStringSubmatch(cc)
	if match == nil {
		match = maxAge.FindStringSubmatch(cc)
	}
	if match == nil {
		return 0
	}
	seconds, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// baseKey builds the cache key in one allocation: a Builder with an exact
// size hint, instead of the chained concatenation that allocated once per
// part. The key shape (method \n path \n query \n host) is unchanged.
func baseKey(r *http.Request) string {
	path := r.URL.EscapedPath()
	var b strings.Builder
	b.Grow(len(r.Method) + len(path) + len(r.URL.RawQuery) + len(r.Host) + 3)
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString(r.URL.RawQuery)
	b.WriteByte('\n')
	b.WriteString(r.Host)
	return b.String()
}
func (c *Cache) get(key string, r *http.Request) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[key]
	if element == nil {
		return nil
	}
	entry := element.Value.(*cacheEntry)
	if time.Now().After(entry.expires) {
		c.remove(element)
		return nil
	}
	for name, value := range entry.variant {
		if r.Header.Get(name) != value {
			return nil
		}
	}
	c.order.MoveToFront(element)
	return entry
}
func (c *Cache) remove(e *list.Element) {
	entry := e.Value.(*cacheEntry)
	delete(c.entries, entry.key)
	c.size -= entry.size
	c.order.Remove(e)
}
func (c *Cache) put(entry *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.size > c.capacity || entry.size > c.maxItem {
		return
	}
	if old := c.entries[entry.key]; old != nil {
		c.remove(old)
	}
	for c.size+entry.size > c.capacity && c.order.Len() > 0 {
		c.remove(c.order.Back())
	}
	c.entries[entry.key] = c.order.PushFront(entry)
	c.size += entry.size
}

// replay writes one stored entry through the response writer: the recorded
// lane first (precomposed head + body, one writev, no map work; see
// recorded.go), then the single-alloc header copy, validator check and body
// write shared by ordinary hits and fixed-route replays. hit reports whether
// the ordinary X-Cache: hit marker applies; fixed replays keep the captured
// X-Cache value, which is the miss marker the application path produced when
// the fixed table was filled.
func replay(w http.ResponseWriter, r *http.Request, entry *cacheEntry, hit bool) {
	if recorded := entry.recorded; recorded != nil && recordedRequestEligible(r) {
		if receiver := findRecordedReceiver(w); receiver != nil {
			status, head, body := entry.status, recorded.head, entry.body
			if recorded.etag != "" && ifNoneMatch(r.Header.Get("If-None-Match"), recorded.etag) {
				status, head, body = http.StatusNotModified, recorded.head304, nil
			}
			_ = receiver.WriteRecorded(status, head, body)
			return
		}
	}
	if marker, ok := w.(finalMarker); ok {
		// The recorded bytes are final; skip the compression wrapper's
		// buffering pass. Its header policy still runs in WriteHeader.
		marker.markFinal()
	}
	h := w.Header()
	// One backing array for every header value slice: the per-header
	// append([]string(nil), values...) copies allocated one slice each.
	count := 0
	for _, values := range entry.header {
		count += len(values)
	}
	joined := make([]string, 0, count)
	for name, values := range entry.header {
		base := len(joined)
		joined = append(joined, values...)
		h[name] = joined[base:]
	}
	if hit {
		// The hit marker must overwrite the captured miss marker written at
		// fill time, so it lands after the header copy.
		h.Set("X-Cache", "hit")
	}
	if etag := entry.header.Get("ETag"); etag != "" && r.Header.Get("If-None-Match") != "" {
		for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
			if strings.TrimSpace(candidate) == etag {
				h.Del("Content-Length")
				w.WriteHeader(304)
				return
			}
		}
	}
	w.WriteHeader(entry.status)
	if r.Method != "HEAD" {
		w.Write(entry.body)
	}
}

// recordResponse streams through and keeps only a bounded copy of cacheable
// bodies. captureAlways forces the copy for the fixed-route fill pass, whose
// response is recorded even though its Cache-Control makes it uncacheable by
// the ordinary cache.
type recordResponse struct {
	http.ResponseWriter
	status        int
	header        http.Header
	body          bytes.Buffer
	max           int64
	ttl           time.Duration
	overflow      bool
	captureAlways bool
	// wireHeader is the second snapshot, taken after the wrapped response
	// policy (PublicCompression) has run its WriteHeader: the map as the wire
	// sees it, including the policy's Vary/Content-Encoding additions. The
	// recorded-replay lane renders its head block from it (recorded.go). It
	// stays nil when the policy encoded a body this capture holds in identity
	// form (the recorded bytes would not be the wire bytes) and for captures
	// that do not participate in the lane.
	wireHeader http.Header
}

// Unwrap deliberately does NOT exist on this wrapper. The web layer's
// precomposed-receiver lookup walks Unwrap chains, and this wrapper sits
// between the public response policy and the application on every public
// miss: were it transparent, a fastserve public listener would let an
// application precomposed write bypass the front cache entirely (its X-Cache
// marker and the capture above). Keeping it opaque pins the public chain's
// semantics; the internal listener has no wrapper here and is unaffected.

func (w *recordResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ttl = lifetime(status, w.Header())
	if w.captureAlways {
		w.ttl = time.Hour
	} else if w.ttl > 0 {
		w.Header().Del("Set-Cookie")
	}
	w.header = w.Header().Clone()
	w.ResponseWriter.WriteHeader(status)
	if w.ttl <= 0 && !w.captureAlways {
		return
	}
	// Post-policy snapshot for the recorded-replay lane (see wireHeader).
	// The policy may have compressed a body the capture holds in identity
	// form; the Content-Encoding delta is the tell, and such a capture must
	// stay on the map path where the wrapper re-encodes it.
	if live := w.Header(); live.Get("Content-Encoding") == w.header.Get("Content-Encoding") {
		w.wireHeader = live.Clone()
	}
}

// wire is the post-policy header snapshot used by the recorded-replay lane,
// with the pre-policy clone as a defensive fallback (a capture whose policy
// mutated nothing is the same map either way).
func (w *recordResponse) wire() http.Header {
	if w.wireHeader != nil {
		return w.wireHeader
	}
	return w.header
}
func (w *recordResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.ttl > 0 && !w.overflow {
		if int64(w.body.Len()+len(b)) <= w.max {
			w.body.Write(b)
		} else {
			w.overflow = true
			w.body.Reset()
		}
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		w.overflow = true
	}
	return n, err
}
func (w *recordResponse) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}
func (c *Cache) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eligible := (r.Method == "GET" || r.Method == "HEAD") && r.Header.Get("Upgrade") == "" && r.Header.Get("Range") == "" && len(r.URL.RequestURI()) <= 2048
		if !eligible || c.capacity <= 0 || c.maxItem <= 0 {
			w.Header().Set("X-Cache", "bypass")
			next.ServeHTTP(w, r)
			return
		}
		// Fixed routes replay a captured response instead of the application;
		// see fixed.go. The lookup is a map miss for every other path.
		if pair := c.pairFor(r); pair != nil {
			if entry := pair.forRequest(r); entry != nil {
				replay(w, r, entry, false)
				return
			}
			// Encoding the pair cannot serve: the application path below
			// answers (its 406 or pass-through policy).
		}
		key := baseKey(r)
		if entry := c.get(key, r); entry != nil {
			replay(w, r, entry, true)
			return
		}
		w.Header().Set("X-Cache", "miss")
		fixed := c.FixedRoutes && fixedCandidate(r) && !c.fixedFailed
		capture := &recordResponse{ResponseWriter: w, max: c.maxItem, captureAlways: fixed}
		next.ServeHTTP(capture, r)
		if capture.status == 0 {
			capture.WriteHeader(200)
		}
		// The first clean unconditional GET of a fixed route completes the
		// table: the application just produced one encoding of the response,
		// the chain runs once more for the other, and later requests replay.
		if fixed {
			c.fillFixed(r, next, capture)
			return
		}
		if capture.ttl <= 0 || capture.overflow {
			return
		}
		variant := map[string]string{}
		names := strings.Split(capture.header.Get("Vary"), ",")
		sort.Strings(names)
		size := int64(len(key) + capture.body.Len() + 256)
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" {
				variant[name] = r.Header.Get(name)
				size += int64(len(name) + len(variant[name]))
			}
		}
		for name, values := range capture.header {
			for _, value := range values {
				size += int64(len(name) + len(value))
			}
		}
		body := bytes.Clone(capture.body.Bytes())
		entry := &cacheEntry{key: key, header: capture.header, body: body, status: capture.status, variant: variant, expires: time.Now().Add(capture.ttl), size: size}
		entry.recorded = newRecordedResponse(capture.wire(), capture.status, body, "hit")
		c.put(entry)
	})
}
