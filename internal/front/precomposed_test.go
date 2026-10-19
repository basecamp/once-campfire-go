package front

// Tests for the public chain's precomposed seam (precomposed.go): the front's
// capture wrapper must be transparent to the web layer's head lane exactly when
// the chain can emit a precomposed response, and the emitted head must equal
// the map path's byte for byte — including the front's own X-Cache marker,
// which the seam splices into the block, and the compression policy's Vary,
// which the policy wrapper splices on the 304 block.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// precomposedStub mimics the web application's ENGINE-49 gate at the cache
// seam: when the writer chain ends in a precomposed receiver it emits a head
// block and body through it, otherwise it writes the identical response through
// the http.Header map. head is the map path's sorted lines minus the front's
// X-Cache marker, which the seam must supply.
type precomposedStub struct {
	status int
	head   []byte
	body   []byte
	calls  atomic.Int32
}

func (s *precomposedStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	// The application sets the coding in the map before its own gate, so the
	// front middleware sees it on both paths (the framed head carries it too;
	// the 304 head does not, and the seam supplies it from the map).
	h.Set("Content-Encoding", "gzip")
	if receiver := findPrecomposedReceiver(w); receiver != nil {
		s.calls.Add(1)
		var body [][]byte
		if len(s.body) > 0 {
			body = append(body, s.body)
		}
		_ = receiver.WritePrecomposed(s.status, s.head, body)
		return
	}
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Permitted-Cross-Domain-Policies", "none")
	h.Set("X-Rev", "0")
	h.Set("X-Version", "Go")
	h.Set("X-XSS-Protection", "0")
	if s.status == http.StatusNotModified {
		// The application's map-path 304: the entity headers are suppressed by
		// the status, the coding stays as the middleware set it.
		h.Set("Cache-Control", "no-cache")
		w.WriteHeader(s.status)
		return
	}
	h.Set("Cache-Control", "max-age=0, private, must-revalidate")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Etag", `W/"0f1e2d3c4b5a69788796a5b4c3d2e1f0"`)
	h.Set("Content-Length", strconv.Itoa(len(s.body)))
	w.WriteHeader(s.status)
	w.Write(s.body)
}

const stubHeadPrefix = "Cache-Control: max-age=0, private, must-revalidate\r\n" +
	"Content-Encoding: gzip\r\n" +
	"Content-Length: 7\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Etag: W/\"0f1e2d3c4b5a69788796a5b4c3d2e1f0\"\r\n" +
	"Referrer-Policy: strict-origin-when-cross-origin\r\n" +
	"Vary: Accept-Encoding\r\n"

const stubSecurity = "X-Content-Type-Options: nosniff\r\n" +
	"X-Frame-Options: SAMEORIGIN\r\n" +
	"X-Permitted-Cross-Domain-Policies: none\r\n" +
	"X-Rev: 0\r\n" +
	"X-Version: Go\r\n" +
	"X-Xss-Protection: 0\r\n"

// stub200 returns a stub whose head block is the map path's sorted lines minus
// the front's X-Cache marker.
func stub200() *precomposedStub {
	return &precomposedStub{
		status: 200,
		body:   []byte("payload"),
		head:   []byte(stubHeadPrefix + stubSecurity),
	}
}

// stub304 returns a stub whose 304 head block is the map path's suppressed
// block: the entity coding and Vary are supplied by the wrappers (the seam
// splices the map's Content-Encoding, the compression policy splices Vary).
func stub304() *precomposedStub {
	return &precomposedStub{
		status: http.StatusNotModified,
		head: []byte("Cache-Control: no-cache\r\n" +
			"Referrer-Policy: strict-origin-when-cross-origin\r\n" + stubSecurity),
	}
}

// seamChain builds the production public chain over a stub app on the owned
// loop, with the lane reachable (seam) or hidden (an opaque writer, which
// forces the map path).
func seamChain(t *testing.T, stub *precomposedStub, hide bool) string {
	t.Helper()
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = false
	var app http.Handler = stub
	if hide {
		app = hideRecorded(stub)
	}
	return serveLoopHandler(t, PublicCompression(cache.Handler(app), cfg))
}

// seamTestHeader is the live header map of a framed response: the application's
// lines plus the front's own X-Cache marker.
func seamTestHeader() http.Header {
	return http.Header{
		"X-Cache":                           {"miss"},
		"Content-Encoding":                  {"gzip"},
		"Content-Length":                    {"7"},
		"Content-Type":                      {"text/html; charset=utf-8"},
		"Etag":                              {`W/"0f1e2d3c4b5a69788796a5b4c3d2e1f0"`},
		"Referrer-Policy":                   {"strict-origin-when-cross-origin"},
		"Vary":                              {"Accept-Encoding"},
		"X-Content-Type-Options":            {"nosniff"},
		"X-Frame-Options":                   {"SAMEORIGIN"},
		"X-Permitted-Cross-Domain-Policies": {"none"},
		"X-Rev":                             {"0"},
		"X-Version":                         {"Go"},
		"X-Xss-Protection":                  {"0"},
	}
}

// emitterWriter is a terminal precomposed receiver for the wrapper tests: it
// records the emission and writes nothing (the real loop receiver is exercised
// byte-for-byte by the parity tests).
type emitterWriter struct {
	http.ResponseWriter
	calls int
}

func (w *emitterWriter) WritePrecomposed(status int, head []byte, body [][]byte) error {
	w.calls++
	return nil
}

// TestPrecomposedSeamByteParity pins the seam against the map path on the owned
// loop for a 200 and a 304: the lane must be the active writer and the emitted
// bytes must match with only Date masked.
func TestPrecomposedSeamByteParity(t *testing.T) {
	get := "GET /rooms/1 HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n"
	for _, test := range []struct {
		name string
		stub func() *precomposedStub
	}{
		{"200", stub200},
		{"304", stub304},
	} {
		onStub, offStub := test.stub(), test.stub()
		on := seamChain(t, onStub, false)
		off := seamChain(t, offStub, true)
		onWire := rawExchange(t, on, get)
		offWire := rawExchange(t, off, get)
		if onStub.calls.Load() != 1 {
			t.Fatalf("%s: the seam was called %d times, want 1", test.name, onStub.calls.Load())
		}
		if offStub.calls.Load() != 0 {
			t.Fatalf("%s: the hidden chain emitted %d precomposed responses", test.name, offStub.calls.Load())
		}
		if onWire != offWire {
			t.Fatalf("%s: seam vs map mismatch\n--- seam ---\n%s\n--- map ---\n%s", test.name, onWire, offWire)
		}
	}
}

// TestPrecomposedSeamMarkerPosition pins the splice position: the front's
// X-Cache marker must land where the map path's sort puts it (after Vary,
// before the X- security run).
func TestPrecomposedSeamMarkerPosition(t *testing.T) {
	addr := seamChain(t, stub200(), false)
	wire := rawExchange(t, addr, "GET /rooms/1 HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n")
	if !bytes.Contains([]byte(wire), []byte("Vary: Accept-Encoding\nX-Cache: miss\nX-Content-Type-Options: nosniff\n")) {
		t.Fatalf("X-Cache not in the map path's sorted position:\n%s", wire)
	}
}

// TestPrecomposedSeamGatedOnEmitter pins the gate: a chain without a precomposed
// emitter (the net/http target listener behind PublicCompression) must keep the
// opaque capture, so the application takes the map path and the response is
// still complete.
func TestPrecomposedSeamGatedOnEmitter(t *testing.T) {
	stub := stub200()
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = false
	server := httptest.NewServer(PublicCompression(cache.Handler(stub), cfg))
	defer server.Close()

	// The stub declares the coding its map path sets; a raw client keeps the
	// bytes verbatim (the transport would otherwise try to decode the stub's
	// plain body).
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL + "/rooms/1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(body) != "payload" {
		t.Fatalf("status %d body %q", response.StatusCode, body)
	}
	if stub.calls.Load() != 0 {
		t.Fatalf("a chain without an emitter framed %d responses", stub.calls.Load())
	}
	if got := response.Header.Get("X-Cache"); got != "miss" {
		t.Fatalf("X-Cache = %q, want miss", got)
	}
}

// TestPrecomposedSeamSpliceAllocatesNothing pins the lane's allocation
// contract: the block the application carves has spare capacity, so splicing
// the map's residual lines into it is in place and allocation-free.
func TestPrecomposedSeamSpliceAllocatesNothing(t *testing.T) {
	template := stubHeadPrefix + stubSecurity
	h := seamTestHeader()
	// The arena carve's shape: a block with room beyond the head the
	// application appended into it.
	head := make([]byte, 0, 2048)
	var out []byte
	allocs := testing.AllocsPerRun(200, func() {
		head = head[:0]
		head = append(head, template...)
		out = spliceMissingHeaders(head, h, http.StatusOK)
	})
	if allocs != 0 {
		t.Fatalf("splice allocated %v objects per run", allocs)
	}
	added := "X-Cache: miss\r\n"
	if len(out) != len(template)+len(added) {
		t.Fatalf("spliced head length %d, want %d", len(out), len(template)+len(added))
	}
	if &out[0] != &head[0] {
		t.Fatal("splice did not write in place into the carved block")
	}
}

// TestPrecomposedSeamGatedOnEmitterBypass pins the same gate on the bypass
// path (the front cache disabled, or a request it will not serve): the
// application must not be told a precomposed response can be emitted when the
// chain has no emitter, or its body disappears into the forwarding wrapper.
func TestPrecomposedSeamGatedOnEmitterBypass(t *testing.T) {
	for _, test := range []struct {
		name     string
		cache    int64
		headers  map[string]string
		wantBody string
	}{
		{"cache_disabled", 0, nil, "payload"},
		{"range_bypass", 64 << 20, map[string]string{"Range": "bytes=0-3"}, "payload"},
		{"upgrade_bypass", 64 << 20, map[string]string{"Upgrade": "websocket"}, "payload"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := stub200()
			cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: test.cache, MaxCacheItemSize: test.cache}
			cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
			cache.FixedRoutes = false
			server := httptest.NewServer(PublicCompression(cache.Handler(stub), cfg))
			defer server.Close()
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			defer client.CloseIdleConnections()
			request, err := http.NewRequest("GET", server.URL+"/rooms/1", nil)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != 200 || string(body) != test.wantBody {
				t.Fatalf("status %d body %q, want 200 %q", response.StatusCode, body, test.wantBody)
			}
			if stub.calls.Load() != 0 {
				t.Fatalf("a chain without an emitter framed %d responses", stub.calls.Load())
			}
		})
	}
}

// TestPrecomposedSeamTightBlockAllocations pins the tight-block path — a caller
// that built the head itself, without the arena's spare capacity: one
// replacement block serves the whole run, the splice lines and the policy's own
// Vary line included, for both the 200 block and the 304 block (whose map
// carries the coding the block suppresses).
func TestPrecomposedSeamTightBlockAllocations(t *testing.T) {
	h304 := http.Header{
		"X-Cache":                           {"miss"},
		"Content-Encoding":                  {"gzip"},
		"Referrer-Policy":                   {"strict-origin-when-cross-origin"},
		"X-Content-Type-Options":            {"nosniff"},
		"X-Frame-Options":                   {"SAMEORIGIN"},
		"X-Permitted-Cross-Domain-Policies": {"none"},
		"X-Rev":                             {"0"},
		"X-Version":                         {"Go"},
		"X-Xss-Protection":                  {"0"},
	}
	for _, test := range []struct {
		name   string
		block  string
		header http.Header
		status int
	}{
		{"200", stubHeadPrefix + stubSecurity, seamTestHeader(), http.StatusOK},
		{"304", "Cache-Control: no-cache\r\nReferrer-Policy: strict-origin-when-cross-origin\r\n" + stubSecurity, h304, http.StatusNotModified},
	} {
		tight := []byte(test.block) // cap == len: no spare capacity
		var out []byte
		allocs := testing.AllocsPerRun(200, func() {
			out = spliceMissingHeaders(tight, test.header, test.status)
			out = insertHeadLine(out, "Vary", "Accept-Encoding")
		})
		if allocs != 1 {
			t.Fatalf("%s: tight block allocated %v objects, want 1", test.name, allocs)
		}
		for _, want := range []string{"X-Cache: miss\r\n", "Vary: Accept-Encoding\r\n"} {
			if !bytes.Contains(out, []byte(want)) {
				t.Fatalf("%s: spliced block lacks %q:\n%s", test.name, want, out)
			}
		}
	}
}
