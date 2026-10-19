package front

// Benchmarks for the production public chain (forward -> PublicCompression ->
// response cache -> app) on the three auxiliary routes the official harness
// measures. Response shapes mirror the merged-official-20261007 validation
// record: up = 73 B html (uncacheable: the app runs on every request),
// avatar = image/webp 3364 B on a long signed-token path (cache hit after
// warm-up), static_css = text/css 1218 B (cache hit). The loadgen negotiates
// gzip and sends a session cookie on every request.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

const (
	benchUpBody     = `<!DOCTYPE html><html><body style="background-color: green"></body></html>`
	benchAvatarPath = "/users/eyJfcmFpdHMiOnsidG9rZW4iOiIyOTgyOWJjODBlZTcyY2E2NzQyODgxZTlhZWU5NmE1ZmE0ZTZiMDA0OTJiZTJlNTc1MGZhMDQ3MTM5MGM4YjIxIiwiZXhwaXJlc19hdCI6IjIwMjYtMTAtMDdUMjM6NTk6NTlaeiJ9fQ--9e2b0a9d7d47f1693d91df77af215fd9298a9322/avatar"
	benchCSSPath    = "/assets/_reset-9c3efd7b.css"
)

// benchPayloads returns bodies that compress like the real ones: the avatar
// webp compresses almost not at all, the css compresses a little.
func benchPayloads() (avatar, css []byte) {
	avatar = make([]byte, 3364)
	state := uint32(0x9e3779b9)
	for i := range avatar {
		state = state*1664525 + 1013904223
		avatar[i] = byte(state >> 24)
	}
	css = make([]byte, 1218)
	copy(css, "/* reset */\n")
	state = uint32(0x243f6a88)
	for i := 80; i < len(css); i++ {
		state = state*1103515245 + 12345
		css[i] = "abcdefghijklmnopqrstuvwxyz{}:;,#.()\"'0123456789 -"[state%49]
	}
	return avatar, css
}

// benchStub mimics the web application at the cache seam: the same fixed
// headers and body shapes the real handlers produce (ETag/Cache-Control as
// the response buffer emits them; the long signed avatar path).
func benchStub() http.Handler {
	avatar, css := benchPayloads()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /up", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", `W/"19d331d3f0e5bd1e09a578d09add1e2e"`)
		w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
		io.WriteString(w, benchUpBody)
	})
	mux.HandleFunc("GET "+benchAvatarPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=1800, public, stale-while-revalidate=604800")
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Disposition", `inline; filename="jason.webp"`)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Write(avatar)
	})
	mux.HandleFunc("GET "+benchCSSPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("ETag", `W/"dc0fe63af770c33e56e894ddc3f0c17d"`)
		w.Write(css)
	})
	return mux
}

// benchChain builds the production public chain over the stub app: the same
// composition front.Serve makes for the public listener with defaults (gzip
// enabled, jitter 32, 64 MiB cache), minus the engine pass-through and web
// session layers, which internal/web measures separately.
func benchChain(logRequests, gzipEnabled bool) http.Handler {
	cfg := Config{Gzip: gzipEnabled, CompressionJitter: 32, LogRequests: logRequests, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	root := Deflate(benchStub())
	return forward(PublicCompression(NewCache(cfg.CacheSize, cfg.MaxCacheItemSize).Handler(root), cfg), cfg)
}

// benchGet performs one keep-alive GET with the loadgen's header shape and
// returns the raw body bytes (not auto-decoded: we set Accept-Encoding).
func benchGet(b *testing.B, client *http.Client, url, path string, expect int) []byte {
	b.Helper()
	request, err := http.NewRequest("GET", url+path, nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Cookie", "_campfire_session=bench; session_token=bench-token-bench-token-ben")
	request.Header.Set("User-Agent", "loadgen/1.0")
	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		b.Fatal(err)
	}
	if response.StatusCode != 200 {
		b.Fatalf("%s status %d", path, response.StatusCode)
	}
	if got == nil {
		b.Fatalf("%s empty body", path)
	}
	return got
}

// warmCache pulls a route once to fill the cache, then once again to pin the
// steady-state outcome (hit for cacheable routes, miss for /up), and returns
// the steady-state wire body length and whether the body was gzip-encoded.
func warmCache(b *testing.B, client *http.Client, url, path, wantCache string) int {
	b.Helper()
	body := benchGet(b, client, url, path, 0)
	if got := body; len(got) == 0 {
		b.Fatal("fill returned an empty body")
	}
	body = benchGet(b, client, url, path, 0)
	if got := body; len(got) == 0 {
		b.Fatal("steady-state returned an empty body")
	}
	request, err := http.NewRequest("GET", url+path, nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if got := response.Header.Get("X-Cache"); got != wantCache {
		b.Fatalf("%s warm X-Cache = %q, want %q", path, got, wantCache)
	}
	if response.Header.Get("Content-Encoding") != "gzip" {
		b.Fatalf("%s warm Content-Encoding = %q, want gzip", path, response.Header.Get("Content-Encoding"))
	}
	return len(body)
}

func benchServer(b *testing.B, handler http.Handler) (*httptest.Server, *http.Client) {
	b.Helper()
	server := httptest.NewServer(handler)
	b.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	return server, client
}

// BenchmarkFrontUp measures the uncacheable /up through the full public chain
// (the stub app answers every request, like the real health handler).
func BenchmarkFrontUp(b *testing.B) {
	server, client := benchServer(b, benchChain(false, true))
	length := warmCache(b, client, server.URL, "/up", "miss")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := benchGet(b, client, server.URL, "/up", 0)
		if len(body) != length {
			b.Fatalf("up wire body %d bytes, want %d", len(body), length)
		}
	}
}

// BenchmarkFrontAvatarHit measures the cache-hit avatar replay (the steady
// state of the official avatar row).
func BenchmarkFrontAvatarHit(b *testing.B) {
	server, client := benchServer(b, benchChain(false, true))
	length := warmCache(b, client, server.URL, benchAvatarPath, "hit")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := benchGet(b, client, server.URL, benchAvatarPath, 0)
		if len(body) != length {
			b.Fatalf("avatar wire body %d bytes, want %d", len(body), length)
		}
	}
}

// BenchmarkFrontStaticCSSHit measures the cache-hit CSS replay.
func BenchmarkFrontStaticCSSHit(b *testing.B) {
	server, client := benchServer(b, benchChain(false, true))
	length := warmCache(b, client, server.URL, benchCSSPath, "hit")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := benchGet(b, client, server.URL, benchCSSPath, 0)
		if len(body) != length {
			b.Fatalf("css wire body %d bytes, want %d", len(body), length)
		}
	}
}

// BenchmarkFrontUpLogged isolates the per-request request-log cost: same chain
// with LogRequests=true (the production default), drained to io.Discard so the
// benchmark stays readable while the record formatting/write cost is kept.
func BenchmarkFrontUpLogged(b *testing.B) {
	server, client := benchServer(b, benchChain(true, true))
	length := warmCache(b, client, server.URL, "/up", "miss")
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previous)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := benchGet(b, client, server.URL, "/up", 0)
		if len(body) != length {
			b.Fatalf("up wire body %d bytes, want %d", len(body), length)
		}
	}
}

// BenchmarkFrontUpNoGzip isolates the identity negotiation path on the
// uncacheable route (native-harness shape).
func BenchmarkFrontUpNoGzip(b *testing.B) {
	server, client := benchServer(b, benchChain(false, true))
	request, err := http.NewRequest("GET", server.URL+"/up", nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cookie", "_campfire_session=bench; session_token=bench-token-bench-token-ben")
	body, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, body.Body)
	body.Body.Close()
	if body.Header.Get("Content-Encoding") != "" {
		b.Fatalf("identity response encoded %q", body.Header.Get("Content-Encoding"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		got, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			b.Fatal(err)
		}
		if !bytes.Equal(got, []byte(benchUpBody)) {
			b.Fatalf("identity up body changed")
		}
	}
}

// shimWriter is a bare minimum full-response writer for the server-side-only
// benchmarks: it records status and headers and counts body bytes. It
// implements everything the chain touches on the read routes (Header, the
// controller Flush path is not exercised here).
type shimWriter struct {
	header http.Header
	status int
	bytes  int64
}

func (w *shimWriter) Header() http.Header         { return w.header }
func (w *shimWriter) WriteHeader(status int)      { w.status = status }
func (w *shimWriter) Write(p []byte) (int, error) { w.bytes += int64(len(p)); return len(p), nil }

// benchDirectRequest builds one request the way the server would deliver it.
// The header map is fresh per request (the server allocates one per request);
// the URL struct is shared.
func benchDirectRequest(path string, sharedURL *url.URL) *http.Request {
	request := &http.Request{
		Method:     "GET",
		URL:        sharedURL,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       "127.0.0.1:47130",
		RemoteAddr: "127.0.0.1:53100",
		Header: http.Header{
			"Accept-Encoding": {"gzip"},
			"Cookie":          {"_campfire_session=bench; session_token=bench-token-bench-token-ben"},
			"User-Agent":      {"loadgen/1.0"},
		},
		RequestURI: path,
	}
	return request.WithContext(context.Background())
}

// BenchmarkFrontDirectUp measures the pure server-side chain for /up: no
// socket, no client — forward, negotiation, fixed/ordinary cache pass and the
// stub app (or the fixed replay).
func BenchmarkFrontDirectUp(b *testing.B) {
	handler := benchChain(false, true)
	request := benchDirectRequest("/up", &url.URL{Path: "/up"})
	shim := &shimWriter{header: http.Header{}}
	handler.ServeHTTP(shim, request)
	if shim.status != 200 {
		b.Fatal(shim.status)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shim := &shimWriter{header: http.Header{}}
		handler.ServeHTTP(shim, benchDirectRequest("/up", request.URL))
		if shim.status != 200 {
			b.Fatal(shim.status)
		}
	}
}

// BenchmarkFrontDirectAvatarHit and the CSS variant isolate the cache-hit
// replay lane server-side.
func BenchmarkFrontDirectAvatarHit(b *testing.B) {
	handler := benchChain(false, true)
	fill := benchDirectRequest(benchAvatarPath, &url.URL{Path: benchAvatarPath})
	shim := &shimWriter{header: http.Header{}}
	handler.ServeHTTP(shim, fill) // fill
	if shim.status != 200 {
		b.Fatal(shim.status)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shim := &shimWriter{header: http.Header{}}
		handler.ServeHTTP(shim, benchDirectRequest(benchAvatarPath, fill.URL))
		if shim.status != 200 {
			b.Fatal(shim.status)
		}
	}
}

func BenchmarkFrontDirectStaticCSSHit(b *testing.B) {
	handler := benchChain(false, true)
	fill := benchDirectRequest(benchCSSPath, &url.URL{Path: benchCSSPath})
	shim := &shimWriter{header: http.Header{}}
	handler.ServeHTTP(shim, fill) // fill
	if shim.status != 200 {
		b.Fatal(shim.status)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shim := &shimWriter{header: http.Header{}}
		handler.ServeHTTP(shim, benchDirectRequest(benchCSSPath, fill.URL))
		if shim.status != 200 {
			b.Fatal(shim.status)
		}
	}
}
