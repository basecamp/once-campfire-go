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
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// sidebarPageEntry is one fully assembled sidebar response: the plain body
// (for identity clients) and its gzip member (for gzip clients), plus the weak
// validator the Map path computes over the same bytes.
type sidebarPageEntry struct {
	key   string
	plain []byte
	gzip  []byte
	etag  string
	bytes int
}

// sidebarPageCache is a bounded LRU of whole sidebar responses, with the same
// retention/accounting shape as the fragment cache (packed entries, prune to
// 3/4 on overflow, oversized entries rejected).
type sidebarPageCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	limit   int
	size    int
}

func newSidebarPageCache(limit int) *sidebarPageCache {
	return &sidebarPageCache{limit: limit, entries: make(map[string]*list.Element)}
}

// sidebarPageKey names one user's whole sidebar page at one gate version. The
// frame mode is part of the key: a Turbo-Frame GET renders the frame-only
// shell (layout-start's .Frame branch), a different document. The user-row
// markers the document renders are part of the key too: the session user is
// read fresh on every request (sessionState), so a user-row write outside the
// audited helpers — which do not bump the gate — still changes the key and
// the page re-renders. The account row is not in the key: every audited
// account write bumps the gate, and the read cache carries the same
// version-only limit for un-audited account SQL.
func sidebarPageKey(version uint64, u database.User, frame bool) string {
	if frame {
		return fmt.Sprintf("sidebar-page/%d/%d/%d/%s/%d/frame", version, u.ID, u.UpdatedAt.UnixMicro(), u.Name, u.Role)
	}
	return fmt.Sprintf("sidebar-page/%d/%d/%d/%s/%d", version, u.ID, u.UpdatedAt.UnixMicro(), u.Name, u.Role)
}

func (c *sidebarPageCache) get(key string) (sidebarPageEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.MoveToFront(element)
		return element.Value.(sidebarPageEntry), true
	}
	return sidebarPageEntry{}, false
}

func (c *sidebarPageCache) put(entry sidebarPageEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[entry.key]; ok {
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
		delete(c.entries, old.key)
		c.size -= old.bytes
		c.order.Remove(oldest)
	}
	entry.bytes = size
	c.entries[entry.key] = c.order.PushFront(entry)
	c.size += size
}

// storeSidebarPage caches the just-rendered sidebar page under the served gate
// version. Every page input is covered by that version, so the store happens
// on any full render — gate miss or a warm layout render that found no page —
// and the same bytes then serve every request until the next sidebar-visible
// write. body aliases the render buffer, so the entry copies what it keeps.
func (s *Server) storeSidebarPage(p page, body []byte) {
	key := sidebarPageKey(p.gateVersion, p.User, p.Frame)
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, 6)
	writer.Header.OS = 3
	_, _ = writer.Write(body)
	_ = writer.Close()
	sum := sha256.Sum256(body)
	entry := sidebarPageEntry{
		key:   key,
		plain: bytes.Clone(body),
		gzip:  compressed.Bytes(),
		etag:  fmt.Sprintf("W/\"%x\"", sum[:16]),
	}
	s.sidebarPages.put(entry)
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
	w.WriteHeader(http.StatusOK)
	if s.clientEncoding(r) == "gzip" {
		h.Set("Content-Encoding", "gzip")
		_, _ = w.Write(entry.gzip)
		return
	}
	_, _ = w.Write(entry.plain)
}
