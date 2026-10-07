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
}
type Cache struct {
	mu                      sync.Mutex
	entries                 map[string]*list.Element
	order                   *list.List
	size, capacity, maxItem int64
}

func NewCache(capacity, maxItem int64) *Cache {
	return &Cache{entries: map[string]*list.Element{}, order: list.New(), capacity: capacity, maxItem: maxItem}
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
func baseKey(r *http.Request) string {
	return r.Method + "\n" + r.URL.EscapedPath() + "\n" + r.URL.RawQuery + "\n" + r.Host
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

// recordResponse streams through and keeps only a bounded copy of cacheable bodies.
type recordResponse struct {
	http.ResponseWriter
	status   int
	header   http.Header
	body     bytes.Buffer
	max      int64
	ttl      time.Duration
	overflow bool
}

func (w *recordResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *recordResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ttl = lifetime(status, w.Header())
	if w.ttl > 0 {
		w.Header().Del("Set-Cookie")
		w.header = w.Header().Clone()
	}
	w.ResponseWriter.WriteHeader(status)
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
		key := baseKey(r)
		if entry := c.get(key, r); entry != nil {
			for name, values := range entry.header {
				w.Header()[name] = append([]string(nil), values...)
			}
			w.Header().Set("X-Cache", "hit")
			if etag := entry.header.Get("ETag"); etag != "" {
				for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
					if strings.TrimSpace(candidate) == etag {
						w.Header().Del("Content-Length")
						w.WriteHeader(304)
						return
					}
				}
			}
			w.WriteHeader(entry.status)
			if r.Method != "HEAD" {
				w.Write(entry.body)
			}
			return
		}
		w.Header().Set("X-Cache", "miss")
		capture := &recordResponse{ResponseWriter: w, max: c.maxItem}
		next.ServeHTTP(capture, r)
		if capture.status == 0 {
			capture.WriteHeader(200)
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
		c.put(&cacheEntry{key: key, header: capture.header, body: bytes.Clone(capture.body.Bytes()), status: capture.status, variant: variant, expires: time.Now().Add(capture.ttl), size: size})
	})
}
