package front

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// BenchmarkFrontDirectPrivateMiss measures the public chain for an uncacheable
// application response with the fixed table off, so the capture path runs on
// every iteration: the forward request edits, the public compression policy,
// the capture wrapper (whose header-map clone is skipped when the response is
// uncacheable — the shape of every application page) and the stub app. No
// socket, so the measurement is the server-side chain alone.
func BenchmarkFrontDirectPrivateMiss(b *testing.B) {
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = false
	handler := forward(PublicCompression(cache.Handler(Deflate(benchStub())), cfg), cfg)
	request := benchDirectRequest("/up", &url.URL{Path: "/up"})
	shim := &shimWriter{header: http.Header{}}
	handler.ServeHTTP(shim, request)
	if shim.status != 200 {
		b.Fatal(shim.status)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shim := &shimWriter{header: http.Header{}}
		handler.ServeHTTP(shim, benchDirectRequest("/up", request.URL))
		if shim.status != 200 {
			b.Fatal(shim.status)
		}
	}
}

// BenchmarkFrontDirectPrivateMissLogged is the same chain with the production
// log default on and a no-op sink: it isolates the per-request branch and
// duration read the access-log record costs on the uncacheable path (the
// production sink is the async queue push, cmd/campfire/accesslog.go).
func BenchmarkFrontDirectPrivateMissLogged(b *testing.B) {
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: true, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	cfg.AccessLog = func(string, string, time.Duration) {}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = false
	handler := forward(PublicCompression(cache.Handler(Deflate(benchStub())), cfg), cfg)
	request := benchDirectRequest("/up", &url.URL{Path: "/up"})
	shim := &shimWriter{header: http.Header{}}
	handler.ServeHTTP(shim, request)
	if shim.status != 200 {
		b.Fatal(shim.status)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shim := &shimWriter{header: http.Header{}}
		handler.ServeHTTP(shim, benchDirectRequest("/up", request.URL))
		if shim.status != 200 {
			b.Fatal(shim.status)
		}
	}
}

// benchEmitter is a terminal precomposed receiver for the direct benchmarks:
// it accepts the emission and allocates nothing per call, so a chain built on
// it is emitter-backed (the real loop receiver is exercised byte-for-byte by
// the parity tests).
type benchEmitter struct {
	header http.Header
	status int
	calls  int
}

func (w *benchEmitter) Header() http.Header         { return w.header }
func (w *benchEmitter) WriteHeader(status int)      { w.status = status }
func (w *benchEmitter) Write(p []byte) (int, error) { return len(p), nil }
func (w *benchEmitter) WritePrecomposed(status int, head []byte, body [][]byte) error {
	w.status, w.calls = status, w.calls+1
	return nil
}

// BenchmarkFrontDirectBypass measures the bypass path on an emitter-backed
// chain: with the front cache disabled every request takes it, and the front
// installs its precomposed surface there. Both surfaces are fields of the
// request's policy wrapper, so the installation allocates nothing (before the
// fix it allocated one wrapper per request). The stub app writes the map path,
// so what remains is the front's own work.
func BenchmarkFrontDirectBypass(b *testing.B) {
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 0, MaxCacheItemSize: 0}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = false
	handler := forward(PublicCompression(cache.Handler(Deflate(benchStub())), cfg), cfg)
	request := benchDirectRequest("/up", &url.URL{Path: "/up"})
	emitter := &benchEmitter{header: http.Header{}}
	handler.ServeHTTP(emitter, request)
	if emitter.status != 200 {
		b.Fatal(emitter.status)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emitter.status = 0
		handler.ServeHTTP(emitter, benchDirectRequest("/up", request.URL))
		if emitter.status != 200 {
			b.Fatal(emitter.status)
		}
	}
}
