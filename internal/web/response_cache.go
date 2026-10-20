package web

import (
	"bytes"
	"container/list"
	"net/http"
	"strings"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

type cachedResponse struct {
	key     string
	version uint64
	body    responsebody.Part
	header  http.Header
	cost    int
}
type responseCache struct {
	mu           sync.Mutex
	entries      map[string]*list.Element
	order        list.List
	limit, size  int
	version      uint64
	hits, misses uint64
}
type responseRound struct {
	version          uint64
	user             int64
	key              string
	gzip, hit, flash bool
}

func newResponseCache(limit int) *responseCache {
	return &responseCache{limit: limit, entries: make(map[string]*list.Element)}
}

func (c *responseCache) get(key string, version uint64) *cachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if version < c.version {
		c.misses++
		return nil
	}
	if version != c.version {
		c.entries = make(map[string]*list.Element)
		c.order.Init()
		c.size = 0
		c.version = version
	}
	if element := c.entries[key]; element != nil {
		c.order.MoveToFront(element)
		c.hits++
		return element.Value.(*cachedResponse)
	}
	c.misses++
	return nil
}

func (c *responseCache) put(entry *cachedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.version != c.version || len(entry.key) > 8192 || entry.cost > c.limit/8 {
		return
	}
	if c.entries[entry.key] != nil {
		return
	}
	for c.size+entry.cost > c.limit && c.order.Len() > 0 {
		oldest := c.order.Back()
		old := oldest.Value.(*cachedResponse)
		delete(c.entries, old.key)
		c.order.Remove(oldest)
		c.size -= old.cost
	}
	c.entries[entry.key] = c.order.PushFront(entry)
	c.size += entry.cost
}

func (s *Server) beginResponseCache(r *http.Request) {
	info := requestMetadata(r.Context())
	if info == nil {
		return
	}
	version, err := s.DB.ResponseVersion(r.Context())
	if err != nil {
		return
	}
	info.databaseVersion = version
	if s.responses.limit == 0 || (r.Method != "GET" && r.Method != "HEAD") ||
		r.ContentLength != 0 ||
		len(r.TransferEncoding) != 0 {
		return
	}
	route, _, err := recognizeRequest(r)
	if err != nil || route == nil {
		return
	}
	switch route.Endpoint {
	case "rooms#show", "messages#index", "users/sidebars#show", "searches#index":
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
		version: version,
		gzip:    encoding == "gzip",
		flash:   state.values["flash"] != nil,
	}
}

func (s *Server) responseHit(r *http.Request) *cachedResponse {
	info := requestMetadata(r.Context())
	if info == nil || info.response == nil {
		return nil
	}
	round := info.response
	if round.flash || round.user == 0 {
		return nil
	}
	version, err := s.DB.ResponseVersion(r.Context())
	if err != nil || version != round.version {
		return nil
	}
	round.key = responseKey(r, info, round.user, round.gzip)
	if round.key == "" {
		return nil
	}
	entry := s.responses.get(round.key, version)
	round.hit = entry != nil
	return entry
}

func (entry *cachedResponse) serve(w http.ResponseWriter) {
	for key, values := range entry.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	writeParts(w, http.StatusOK, []responsebody.Part{entry.body})
}

// Only completed representations enter the cache; security and session headers
// stay on the live writer. The version was captured before authentication.
// Coding has already selected immutable bytes; this cache does not encode them.
func (s *Server) cacheResponse(r *http.Request, w *responseBuffer, response front.CompletedResponse) front.CompletedResponse {
	info := requestMetadata(r.Context())
	if info == nil || info.response == nil {
		return response
	}
	round := info.response
	if round.hit || round.flash || round.key == "" || r.Method != "GET" || response.Status != 200 ||
		w.exception || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") ||
		strings.Contains(w.Header().Get("Cache-Control"), "no-store") ||
		strings.Contains(w.Header().Get("Cache-Control"), "no-transform") {
		return response
	}
	version, err := s.DB.ResponseVersion(r.Context())
	if err != nil || version != round.version {
		return response
	}
	size := 0
	for _, part := range response.Parts {
		size += part.Len()
	}
	if size+len(round.key)+1024 > s.responses.limit/8 {
		return response
	}
	var body bytes.Buffer
	body.Grow(size)
	for _, part := range response.Parts {
		if _, err = part.WriteTo(&body); err != nil {
			return response
		}
	}
	header := make(http.Header)
	for _, name := range []string{"Content-Type", "Content-Encoding", "ETag", "Last-Modified", "Cache-Control", "Link", "Vary"} {
		if values := w.Header().Values(name); len(values) > 0 {
			header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	entry := &cachedResponse{
		key:     round.key,
		version: round.version,
		body:    responsebody.NewPart(body.Bytes()),
		header:  header,
	}
	entry.cost = body.Cap() + len(entry.key) + 1024
	for name, values := range header {
		entry.cost += len(name)
		for _, value := range values {
			entry.cost += len(value)
		}
	}
	version, err = s.DB.ResponseVersion(r.Context())
	if err == nil && version == round.version {
		s.responses.put(entry)
	}
	response.Parts = []responsebody.Part{entry.body}
	return response
}
