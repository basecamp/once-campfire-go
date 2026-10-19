package web

// The sidebar whole-page cache: the assembled sidebar document —
// layout around the cached frame — fully rendered and precompressed, keyed by
// (gate version, user, frame mode). A warm request serves the page bytes with
// no template execution and no per-request gzip; the served stream is
// byte-identical to what the same request would have streamed fresh, because
// the member uses the front compressor's own parameters (level 6, OS=3,
// MTIME=0 — the page carries no Last-Modified).
//
// The gate version (internal/database/versions.go) is bumped by every
// sidebar-visible write — user/account rows, custom styles, room and
// membership state, unread changes — so the key covers every page input and
// needs no separate invalidation: a write moves the version, the new key fills
// on the next render, and the old entry ages out of the LRU. The frame gate
// and the read cache carry the same per-process limit for SQL outside the
// audited write helpers (documented in README.md); un-audited writes are
// picked up on restart, not live.

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// sidebarPageEntry is one fully assembled sidebar response: the plain body
// (for identity clients) and its gzip member (for gzip clients), plus the weak
// validator the Map path computes over the same bytes. digest is sha256(key)
// and key is the exact key bytes, so a digest hit can be confirmed exactly.
type sidebarPageEntry struct {
	digest [32]byte
	key    []byte
	plain  []byte
	gzip   []byte
	etag   string
	bytes  int
}

// sidebarPageCache is a bounded LRU of whole sidebar responses, with the same
// retention/accounting shape as the fragment cache (packed entries, prune to
// 3/4 on overflow, oversized entries rejected). Entries are keyed by the
// sha256 digest of the exact key bytes.
type sidebarPageCache struct {
	mu      sync.Mutex
	entries map[[32]byte]*list.Element
	order   list.List
	limit   int
	size    int
}

func newSidebarPageCache(limit int) *sidebarPageCache {
	return &sidebarPageCache{limit: limit, entries: make(map[[32]byte]*list.Element)}
}

// maxSidebarPageKeySize bounds the stack buffer the sidebar page key is built
// into. A user name long enough to exceed it is bypassed by
// sidebarPageKeySize before the builder runs, so no append grows the buffer.
const maxSidebarPageKeySize = 256

// sidebarPageKeySize computes the exact byte length appendSidebarPageKey would
// append and reports whether it fits the stack buffer. It mirrors the
// builder's encoding; TestSidebarPageKeyEnvelope pins the two together.
func sidebarPageKeySize(version, generation uint64, u database.User, frame bool) (int, bool) {
	var num [20]byte
	digits := func(v int64) int { return len(strconv.AppendInt(num[:0], v, 10)) }
	udigits := func(v uint64) int { return len(strconv.AppendUint(num[:0], v, 10)) }
	size := len("sidebar-page/") + udigits(version) + 1 + udigits(generation) + 1 + digits(u.ID) + 1 + digits(u.UpdatedAt.UnixMicro()) + 1 + digits(int64(len(u.Name))) + 1 + len(u.Name) + 1 + digits(int64(u.Role))
	if frame {
		size += len("/frame")
	}
	return size, size <= maxSidebarPageKeySize
}

// appendSidebarPageKey appends the exact key bytes for one user's whole
// sidebar page at one gate version to b. The frame mode is part of the key: a
// Turbo-Frame GET renders the frame-only shell (layout-start's .Frame branch),
// a different document. The user-row markers the document renders are part of
// the key too: the session user is read fresh on every request
// (sessionState), so a user-row write outside the audited helpers — which do
// not bump the gate — still changes the key and the page re-renders. The
// account row is not in the key: every audited account write bumps the gate,
// and the read cache carries the same version-only limit for un-audited
// account SQL. Every string component is length-prefixed so the encoding is
// injective. The caller hashes the returned bytes into a [32]byte map key and
// keeps them for exact verification; building the key allocates nothing for
// the names sidebarPageKeySize admits, and only an admission copies it.
func appendSidebarPageKey(b []byte, version, generation uint64, u database.User, frame bool) []byte {
	b = append(b, "sidebar-page/"...)
	b = strconv.AppendUint(b, version, 10)
	b = append(b, '/')
	b = strconv.AppendUint(b, generation, 10)
	b = append(b, '/')
	b = strconv.AppendInt(b, u.ID, 10)
	b = append(b, '/')
	b = strconv.AppendInt(b, u.UpdatedAt.UnixMicro(), 10)
	b = append(b, '/')
	b = strconv.AppendInt(b, int64(len(u.Name)), 10)
	b = append(b, ':')
	b = append(b, u.Name...)
	b = append(b, '/')
	b = strconv.AppendInt(b, int64(u.Role), 10)
	if frame {
		b = append(b, "/frame"...)
	}
	return b
}

func (c *sidebarPageCache) get(key []byte) (sidebarPageEntry, bool) {
	digest := sha256.Sum256(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[digest]
	if !ok {
		return sidebarPageEntry{}, false
	}
	entry := element.Value.(sidebarPageEntry)
	if !bytes.Equal(entry.key, key) {
		return sidebarPageEntry{}, false
	}
	c.order.MoveToFront(element)
	return entry, true
}

func (c *sidebarPageCache) put(entry sidebarPageEntry) {
	entry.digest = sha256.Sum256(entry.key)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[entry.digest]; ok {
		c.order.MoveToFront(existing)
		return
	}
	size := len(entry.key) + len(entry.plain) + len(entry.gzip) + 240
	if size > c.limit/8 {
		return
	}
	for c.size+size > c.limit && c.order.Len() > 0 {
		oldest := c.order.Back()
		old := oldest.Value.(sidebarPageEntry)
		delete(c.entries, old.digest)
		c.size -= old.bytes
		c.order.Remove(oldest)
	}
	entry.bytes = size
	c.entries[entry.digest] = c.order.PushFront(entry)
	c.size += size
}

// storeSidebarPage caches the just-rendered sidebar page under the served gate
// version. Every page input is covered by that version, so the store happens
// on any full render — gate miss or a warm layout render that found no page —
// and the same bytes then serve every request until the next sidebar-visible
// write. body aliases the render buffer, so the entry copies what it keeps.
func (s *Server) storeSidebarPage(p page, body []byte) (sidebarPageEntry, bool) {
	if s.sidebarPages.limit <= 0 || len(body)+240 > s.sidebarPages.limit/8 {
		return sidebarPageEntry{}, false
	}
	if _, ok := sidebarPageKeySize(p.gateVersion, p.gateGeneration, p.User, p.Frame); !ok {
		// A name too long for the stack buffer is not stored rather than grown.
		return sidebarPageEntry{}, false
	}
	var keyBuf [maxSidebarPageKeySize]byte
	key := appendSidebarPageKey(keyBuf[:0], p.gateVersion, p.gateGeneration, p.User, p.Frame)
	if len(key)+len(body)+240 > s.sidebarPages.limit/8 {
		return sidebarPageEntry{}, false
	}
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, 6)
	writer.Header.OS = 3
	_, _ = writer.Write(body)
	_ = writer.Close()
	sum := sha256.Sum256(body)
	entry := sidebarPageEntry{
		key:   bytes.Clone(key),
		plain: bytes.Clone(body),
		gzip:  compressed.Bytes(),
		etag:  fmt.Sprintf("W/\"%x\"", sum[:16]),
	}
	s.sidebarPages.put(entry)
	return entry, true
}

// serveSidebarPage writes a cached sidebar page: the precomposed headers, the
// format and conditional-get checks, and the body in the client's negotiated
// encoding. The request already carries the pipeline's security headers, so
// only the response-shape headers are set here, exactly like the map path.
func (s *Server) serveSidebarPage(w http.ResponseWriter, r *http.Request, entry sidebarPageEntry) {
	if respondFormat(w, r, "html") == "" {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("ETag", entry.etag)
	h.Set("Cache-Control", "max-age=0, private, must-revalidate")
	if notModified(w, r, entry.etag, time.Time{}) {
		return
	}
	if s.clientEncoding(r) == "gzip" {
		h.Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(entry.gzip)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.plain)
}
