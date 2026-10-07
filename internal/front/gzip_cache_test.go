package front

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func TestGzipCacheExactBytesConcurrentReuseAndBounds(t *testing.T) {
	cache := newGzipCache(16 << 10)
	body := []byte(strings.Repeat("<div>hello &amp; goodbye</div>", 200))
	const clients = 32
	results := make([][]byte, clients)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			results[i], err = cache.prepare(context.Background(), gzipParts(body[:99], body[99:]), 123)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if !bytes.Equal(gunzip(t, results[0]), body) {
		t.Fatal("gzip changed body bytes")
	}
	for _, result := range results {
		if &result[0] != &results[0][0] {
			t.Fatal("concurrent requests did not share immutable gzip bytes")
		}
	}
	whole, err := cache.prepare(context.Background(), gzipParts(body), 123)
	if err != nil || !bytes.Equal(gunzip(t, whole), body) {
		t.Fatal("a different decomposition changed response bytes", err)
	}
	otherTime, err := cache.prepare(context.Background(), gzipParts(body), 456)
	if err != nil || binary.LittleEndian.Uint32(otherTime[4:8]) != 456 || bytes.Equal(otherTime, whole) {
		t.Fatal("gzip mtime not preserved", err)
	}
	for i := 0; i < 200; i++ {
		if _, err := cache.prepare(context.Background(), gzipParts(body), uint32(i)); err != nil {
			t.Fatal(err)
		}
		if cache.size > cache.capacity {
			t.Fatal("cache exceeded byte budget")
		}
	}
	// A large incompressible member must not evict small hot entries. The
	// deterministic bytes make this independent of randomness/compressibility.
	state := uint64(1)
	large := make([]byte, 4096)
	for i := range large {
		state = state*6364136223846793005 + 1442695040888963407
		large[i] = byte(state >> 56)
	}
	before := cache.size
	largeGzip, err := cache.prepare(context.Background(), gzipParts(large), 0)
	if err != nil || !bytes.Equal(gunzip(t, largeGzip), large) || cache.size != before {
		t.Fatal("oversized entry was retained or corrupted", err)
	}
	if len(cache.flights) != 0 {
		t.Fatal("finished work retained flight state")
	}
}

func TestCompletedGzipPreservesPerRequestState(t *testing.T) {
	cache := newGzipCache(gzipCacheBytes)
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	first := []byte(strings.Repeat("private first body", 100))
	second := []byte(strings.Repeat("private other body", 100))
	// With the same weak validator, every part remains an exact-byte dependency.
	for i, data := range [][][]byte{
		{first, first, first}, {first, first, first},
		{second, first, first}, {first, second, first}, {first, first, second},
	} {
		body := bytes.Join(data, nil)
		response := httptest.NewRecorder()
		w := &gzipResponse{ResponseWriter: response, request: request, selected: "gzip", cache: cache}
		w.Header().Set("ETag", `W/"same-resource-version"`)
		w.Header().Set("Cache-Control", "private, max-age=0")
		cookie := "session=" + string(rune('a'+i))
		w.Header().Set("Set-Cookie", cookie)
		w.WriteHeader(200)
		n, err := w.WriteBody(gzipParts(data...))
		if err != nil || n != len(body) || !bytes.Equal(gunzip(t, response.Body.Bytes()), body) {
			t.Fatal("completed body did not roundtrip", err)
		}
		if response.Header().Get("Set-Cookie") != cookie || response.Header().Get("ETag") != `W/"same-resource-version"` {
			t.Fatal("cached response reused headers")
		}
	}
	if cache.order.Len() != 4 {
		t.Fatal("weak ETag reused a different body's gzip")
	}
	for _, test := range []struct{ method, control string }{{"GET", "no-store"}, {"POST", "private"}} {
		request := httptest.NewRequest(test.method, "/", nil)
		response := httptest.NewRecorder()
		w := &gzipResponse{ResponseWriter: response, request: request, selected: "gzip", cache: cache}
		w.Header().Set("Cache-Control", test.control)
		w.WriteHeader(200)
		body := []byte(strings.Repeat("do not retain", 200))
		w.WriteBody(gzipParts(body))
		w.writer.Close()
		w.writer.Reset(nil)
		gzipPool.Put(w.writer)
		if cache.order.Len() != 4 || !bytes.Equal(gunzip(t, response.Body.Bytes()), body) {
			t.Fatal("one-off body was retained or corrupted", test)
		}
	}
}

func TestCompletedAndStreamingGzipLifecycles(t *testing.T) {
	body := strings.Repeat("hello world", 200)
	handler := Deflate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/completed" {
			w.WriteHeader(200)
			w.(interface {
				WriteBody([]responsebody.Part) (int, error)
			}).WriteBody(gzipParts([]byte(body)))
		} else if r.URL.Path == "/stream" {
			io.WriteString(w, body[:10])
			w.(http.Flusher).Flush()
			io.WriteString(w, body[10:])
		} else {
			w.WriteHeader(200)
		}
	}))
	for _, path := range []string{"/completed", "/stream", "/empty"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Accept-Encoding", "gzip")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := body
		if path == "/empty" {
			want = ""
		}
		if string(gunzip(t, response.Body.Bytes())) != want {
			t.Fatal("invalid gzip lifecycle", path)
		}
		// Completed-body hits must not append a second, empty gzip member.
		source := bytes.NewReader(response.Body.Bytes())
		reader, _ := gzip.NewReader(source)
		reader.Multistream(false)
		io.Copy(io.Discard, reader)
		reader.Close()
		if source.Len() != 0 {
			t.Fatal("trailing gzip member", path)
		}
		request.Method = "HEAD"
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Body.Len() != 0 {
			t.Fatal("HEAD sent a body", path)
		}
	}
}

// Only the constructor can attach a digest to completed bytes.
func gzipParts(data ...[]byte) []responsebody.Part {
	parts := make([]responsebody.Part, len(data))
	for i, value := range data {
		parts[i] = responsebody.NewPart(value)
	}
	return parts
}
