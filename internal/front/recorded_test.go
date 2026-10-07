package front

// Differential tests for the ENGINE-62 recorded-replay lane. Every test
// compares the precomposed lane (public chain served by the owned loop)
// against the same chain with the lane hidden — a wrapper that ends the
// Unwrap walk, so the identical header-map path runs — and against the
// historical public path (net/http, no recorded receiver). Raw wire bytes are
// compared with Date masked, so status line, header order, framing (auto
// Content-Length vs chunked) and body must all match.

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/fastserve"
)

// opaqueWriter hides a precomposed/recorded receiver from Unwrap walks while
// forwarding the plain ResponseWriter surface: the chain then runs its
// header-map path on the same loop.
type opaqueWriter struct{ http.ResponseWriter }

func hideRecorded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(opaqueWriter{w}, r)
	})
}

// serveLoopHandler serves one handler on a private fastserve loop.
func serveLoopHandler(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := fastserve.New(handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().String()
}

func recordedParityChain() http.Handler {
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	root := http.Handler(Deflate(benchStub()))
	return forward(PublicCompression(NewCache(cfg.CacheSize, cfg.MaxCacheItemSize).Handler(root), cfg), cfg)
}

func rawExchange(t *testing.T, addr, request string) string {
	t.Helper()
	return string(maskDate(exchange(t, addr, []byte(request))))
}

// TestRecordedLaneByteParity pins the precomposed replay against both the
// same-loop map path and the historical net/http public path across the
// negotiation shapes, HEAD, 304, Range and a keep-alive stream.
func TestRecordedLaneByteParity(t *testing.T) {
	on := serveLoopHandler(t, recordedParityChain())
	off := serveLoopHandler(t, hideRecorded(recordedParityChain()))
	reference := httptest.NewServer(recordedParityChain())
	t.Cleanup(reference.Close)
	ref := reference.Listener.Addr().String()

	get := func(path, encoding string) string {
		request := "GET " + path + " HTTP/1.1\r\nHost: h\r\nConnection: close\r\n"
		if encoding != "" {
			request += "Accept-Encoding: " + encoding + "\r\n"
		}
		return request + "\r\n"
	}

	type wireCase struct {
		name    string
		request string
		warm    int
	}
	cases := []wireCase{
		{"up/gzip", get("/up", "gzip"), 2},
		{"up/identity", get("/up", "identity"), 2},
		{"up/absent", get("/up", ""), 2},
		{"up/zstd_only", get("/up", "zstd"), 2},
		{"up/gzip_zstd", get("/up", "gzip, zstd"), 2},
		{"up/br", get("/up", "br"), 2},
		{"avatar/gzip", get(benchAvatarPath, "gzip"), 3},
		{"avatar/identity", get(benchAvatarPath, "identity"), 3},
		{"avatar/zstd", get(benchAvatarPath, "zstd"), 3},
		{"avatar/gzip_zstd", get(benchAvatarPath, "gzip, zstd"), 3},
		{"css/gzip", get(benchCSSPath, "gzip"), 3},
		{"css/identity", get(benchCSSPath, "identity"), 3},
		{"css/absent", get(benchCSSPath, ""), 3},
		{"css/head", "HEAD " + benchCSSPath + " HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n", 2},
		{"css/range", "GET " + benchCSSPath + " HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nRange: bytes=0-9\r\nConnection: close\r\n\r\n", 1},
		{"css/not_modified", "GET " + benchCSSPath + " HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nIf-None-Match: " + `W/"dc0fe63af770c33e56e894ddc3f0c17d"` + "\r\nConnection: close\r\n\r\n", 2},
		{"avatar/not_modified", "GET " + benchAvatarPath + " HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nIf-None-Match: nope\r\nConnection: close\r\n\r\n", 1},
	}
	for _, test := range cases {
		for i := 0; i < test.warm; i++ {
			exchange(t, on, []byte(test.request))
			exchange(t, off, []byte(test.request))
			exchange(t, ref, []byte(test.request))
		}
		onWire := rawExchange(t, on, test.request)
		offWire := rawExchange(t, off, test.request)
		refWire := rawExchange(t, ref, test.request)
		if onWire != offWire {
			t.Fatalf("%s: recorded lane vs map path mismatch\n--- recorded ---\n%s\n--- map ---\n%s", test.name, onWire, offWire)
		}
		if onWire != refWire {
			t.Fatalf("%s: recorded lane vs net/http mismatch\n--- recorded ---\n%s\n--- net/http ---\n%s", test.name, onWire, refWire)
		}
	}

	// Keep-alive: a multi-request stream on one connection must frame each
	// response the same way the map path does (the chunked avatar and the
	// auto-Content-Length fixed /up share the connection).
	stream := "GET " + benchCSSPath + " HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\n\r\n" +
		"GET " + benchAvatarPath + " HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\n\r\n" +
		"GET /up HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n"
	for i := 0; i < 2; i++ {
		exchange(t, on, []byte(stream))
		exchange(t, off, []byte(stream))
		exchange(t, ref, []byte(stream))
	}
	if want, got := rawExchange(t, off, stream), rawExchange(t, on, stream); want != got {
		t.Fatalf("keep-alive stream: recorded lane vs map path mismatch\n--- recorded ---\n%s\n--- map ---\n%s", got, want)
	}
	if want, got := rawExchange(t, ref, stream), rawExchange(t, on, stream); want != got {
		t.Fatalf("keep-alive stream: recorded lane vs net/http mismatch\n--- recorded ---\n%s\n--- net/http ---\n%s", got, want)
	}
}

// TestRecordedLaneEngages pins that the lane is the active writer for replay
// hits (the recording receiver is called, not the map surface) and that the
// precomposed head carries the map path's exact lines, including the X-Cache
// marker (hit for ordinary entries, the captured miss for the fixed table).
func TestRecordedLaneEngages(t *testing.T) {
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	handler := forward(PublicCompression(cache.Handler(Deflate(benchStub())), cfg), cfg)

	serve := func(path string) *recordingReceiver {
		w := &recordingReceiver{header: http.Header{}}
		r := benchDirectRequest(path, &url.URL{Path: path})
		handler.ServeHTTP(w, r)
		return w
	}
	serve(benchCSSPath) // fill
	for i := 0; i < 3; i++ {
		serve(benchCSSPath)
	}
	calls := serve(benchCSSPath).calls
	if len(calls) != 1 {
		t.Fatalf("hit used WriteRecorded %d times, want 1", len(calls))
	}
	if calls[0].status != 200 {
		t.Fatalf("recorded status %d", calls[0].status)
	}
	if !bytes.Contains(calls[0].head, []byte("X-Cache: hit\r\n")) {
		t.Fatalf("recorded head lacks X-Cache hit:\n%s", calls[0].head)
	}
	if !bytes.Contains(calls[0].head, []byte("Etag: W/\"dc0fe63af770c33e56e894ddc3f0c17d\"\r\n")) {
		t.Fatalf("recorded head lacks the captured ETag:\n%s", calls[0].head)
	}
	if !bytes.Contains(calls[0].head, []byte("Vary: Accept-Encoding\r\n")) {
		t.Fatalf("recorded head lacks the capture's Vary:\n%s", calls[0].head)
	}
	if len(calls[0].body) == 0 {
		t.Fatal("recorded replay carried no body")
	}

	// The avatar entry (an encoded body over the 2048-byte auto-Content-Length
	// budget) must take the lane too (the receiver frames it as one chunk).
	serve(benchAvatarPath)
	serve(benchAvatarPath)
	avatar := serve(benchAvatarPath).calls
	if len(avatar) != 1 || len(avatar[0].body) <= 2048 {
		t.Fatalf("avatar recorded calls %d body %d, want one over-budget replay", len(avatar), len(avatar[0].body))
	}

	// A matching If-None-Match uses the precomposed 304 block: no entity
	// lines survive (Content-Type/Content-Length/Transfer-Encoding excluded).
	conditional := &recordingReceiver{header: http.Header{}}
	r := benchDirectRequest(benchCSSPath, &url.URL{Path: benchCSSPath})
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("If-None-Match", `W/"dc0fe63af770c33e56e894ddc3f0c17d"`)
	handler.ServeHTTP(conditional, r)
	if len(conditional.calls) != 1 || conditional.calls[0].status != http.StatusNotModified {
		t.Fatalf("conditional replay calls %d status %v, want one 304", len(conditional.calls), conditional.calls)
	}
	for _, banned := range []string{"Content-Type:", "Content-Length:", "Transfer-Encoding:"} {
		if bytes.Contains(conditional.calls[0].head, []byte(banned)) {
			t.Fatalf("304 head carries %s:\n%s", banned, conditional.calls[0].head)
		}
	}

	// The fixed /up table replays its captured miss marker.
	serve("/up")
	serve("/up")
	up := serve("/up").calls
	if len(up) != 1 {
		t.Fatalf("fixed replay used WriteRecorded %d times, want 1", len(up))
	}
	if !bytes.Contains(up[0].head, []byte("X-Cache: miss\r\n")) {
		t.Fatalf("fixed recorded head lacks the captured X-Cache miss:\n%s", up[0].head)
	}
}

// recordingReceiver is a scriptable recorded receiver: the map surface
// records nothing that matters (the fill runs through it), while
// WriteRecorded captures the precomposed call.
type recordingReceiver struct {
	header http.Header
	status int
	body   bytes.Buffer
	calls  []recordedCall
}

type recordedCall struct {
	status int
	head   []byte
	body   []byte
}

func (w *recordingReceiver) Header() http.Header    { return w.header }
func (w *recordingReceiver) WriteHeader(status int) { w.status = status }
func (w *recordingReceiver) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func (w *recordingReceiver) WriteRecorded(status int, head []byte, body []byte) error {
	w.calls = append(w.calls, recordedCall{status: status, head: bytes.Clone(head), body: bytes.Clone(body)})
	return nil
}
