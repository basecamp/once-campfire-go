package web

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

type cacheStats struct {
	shellHit, shellMiss     atomic.Uint64
	listHit, listMiss       atomic.Uint64
	sidebarHit, sidebarMiss atomic.Uint64
	searchHit, searchMiss   atomic.Uint64
}

type renderedPage struct {
	key        string
	body       []byte
	gzip, zstd []byte
	etag       string
	bytes      int
}

type renderedPages struct {
	mu           sync.Mutex
	items        map[string]*list.Element
	order        list.List
	bytes, limit int
}

func newRenderedPages(limit int) *renderedPages {
	return &renderedPages{items: map[string]*list.Element{}, limit: limit}
}

func (c *renderedPages) get(key string) (renderedPage, bool) {
	if c == nil {
		return renderedPage{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return renderedPage{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(renderedPage), true
}

func (c *renderedPages) put(key string, body []byte) {
	if c == nil || len(body) == 0 || len(body) > c.limit/4 {
		return
	}
	sum := sha256.Sum256(body)
	page := renderedPage{key: key, body: append([]byte(nil), body...), etag: fmt.Sprintf("W/\"%x\"", sum[:16])}
	if len(body) >= 1024 {
		page.gzip = gzipMember(body)
		page.zstd = zstdMember(body)
	}
	page.bytes = len(page.body) + len(page.gzip) + len(page.zstd) + len(key) + 64
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.items[key]; ok {
		c.bytes -= old.Value.(renderedPage).bytes
		c.order.Remove(old)
		delete(c.items, key)
	}
	for c.bytes+page.bytes > c.limit && c.order.Len() > 0 {
		back := c.order.Back()
		prev := back.Value.(renderedPage)
		c.bytes -= prev.bytes
		delete(c.items, prev.key)
		c.order.Remove(back)
	}
	if c.bytes+page.bytes > c.limit {
		return
	}
	c.items[key] = c.order.PushFront(page)
	c.bytes += page.bytes
}

func (s *Server) writeCached(w http.ResponseWriter, r *http.Request, kind, key string) bool {
	page, ok := s.pages.get(key)
	counterHit, counterMiss := &s.stats.sidebarHit, &s.stats.sidebarMiss
	if kind == "search" {
		counterHit, counterMiss = &s.stats.searchHit, &s.stats.searchMiss
	}
	if !ok {
		counterMiss.Add(1)
		return false
	}
	counterHit.Add(1)
	body := page.body
	if enc := responseEncoding(w, r); enc != "" {
		if encoded := encodedBytes(page.body, page.gzip, page.zstd, enc); len(encoded) > 0 {
			body = encoded
			w.Header().Set("Content-Encoding", enc)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", page.etag)
	w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(200)
	if r.Method != "HEAD" {
		w.Write(body)
	}
	return true
}

func (s *Server) saveCached(w http.ResponseWriter, key string) {
	if status, body := responseSnapshot(w); status == 200 && len(body) > 0 {
		s.pages.put(key, body)
	}
}

func responseSnapshot(w http.ResponseWriter) (int, []byte) {
	for {
		if buffered, ok := w.(*responseBuffer); ok {
			status := buffered.status
			if status == 0 {
				status = 200
			}
			if len(buffered.parts) > 0 {
				size := 0
				for _, part := range buffered.parts {
					size += len(part)
				}
				body := make([]byte, 0, size)
				for _, part := range buffered.parts {
					body = append(body, part...)
				}
				return status, body
			}
			if buffered.body != nil {
				return status, buffered.body.Bytes()
			}
			return status, nil
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return 0, nil
		}
		w = wrapper.Unwrap()
	}
}

type statView struct {
	Hits   uint64  `json:"hits"`
	Misses uint64  `json:"misses"`
	Ratio  float64 `json:"ratio"`
}

func ratio(hits, misses uint64) float64 {
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

func (s *Server) cacheSnapshot() map[string]statView {
	pageHits, pageMisses := s.DB.PageCacheStats()
	return map[string]statView{
		"room_shell":     {s.stats.shellHit.Load(), s.stats.shellMiss.Load(), ratio(s.stats.shellHit.Load(), s.stats.shellMiss.Load())},
		"message_list":   {s.stats.listHit.Load(), s.stats.listMiss.Load(), ratio(s.stats.listHit.Load(), s.stats.listMiss.Load())},
		"message_window": {pageHits, pageMisses, ratio(pageHits, pageMisses)},
		"sidebar":        {s.stats.sidebarHit.Load(), s.stats.sidebarMiss.Load(), ratio(s.stats.sidebarHit.Load(), s.stats.sidebarMiss.Load())},
		"search":         {s.stats.searchHit.Load(), s.stats.searchMiss.Load(), ratio(s.stats.searchHit.Load(), s.stats.searchMiss.Load())},
	}
}

func (s *Server) ResetCacheStats() {
	s.stats.shellHit.Store(0)
	s.stats.shellMiss.Store(0)
	s.stats.listHit.Store(0)
	s.stats.listMiss.Store(0)
	s.stats.sidebarHit.Store(0)
	s.stats.sidebarMiss.Store(0)
	s.stats.searchHit.Store(0)
	s.stats.searchMiss.Store(0)
	s.DB.ResetPageStats()
}
