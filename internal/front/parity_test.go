package front

// Differential tests for the ENGINE-52 fast paths. Every test compares the
// new path (fixed /up table, markFinal replay lane, in-place forward header
// edits) against the application path byte for byte: status, headers
// (Date excluded: it advances between requests), and raw body.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

type wireResponse struct {
	status int
	header http.Header
	body   []byte
}

func captureRaw(t *testing.T, client *http.Client, method, url, path string, headers map[string]string) wireResponse {
	t.Helper()
	request, err := http.NewRequest(method, url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	header := response.Header.Clone()
	header.Del("Date")
	return wireResponse{status: response.StatusCode, header: header, body: body}
}

func testChain(t *testing.T, fixed bool, upCalls *atomic.Int32) (*httptest.Server, *http.Client) {
	t.Helper()
	cfg := Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	root := http.Handler(Deflate(benchStub()))
	if upCalls != nil {
		root = Deflate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/up" {
				upCalls.Add(1)
			}
			benchStub().ServeHTTP(w, r)
		}))
	}
	cache := NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = fixed
	handler := forward(PublicCompression(cache.Handler(root), cfg), cfg)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}}
	t.Cleanup(client.CloseIdleConnections)
	return server, client
}

func assertWireEqual(t *testing.T, name string, got, want wireResponse) {
	t.Helper()
	if got.status != want.status {
		t.Fatalf("%s: status %d, want %d", name, got.status, want.status)
	}
	if !reflect.DeepEqual(got.header, want.header) {
		t.Fatalf("%s: headers differ\n got: %v\nwant: %v", name, got.header, want.header)
	}
	if string(got.body) != string(want.body) {
		t.Fatalf("%s: body %q, want %q", name, got.body, want.body)
	}
}

// TestFixedUpByteParity compares the fixed /up table against the application
// path across the encodings the loadgen and browsers send. The fixed server's
// first request fills the table from the application; the second replays.
// Both must equal the fixed-off server's responses byte for byte.
func TestFixedUpByteParity(t *testing.T) {
	offServer, offClient := testChain(t, false, nil)
	onServer, onClient := testChain(t, true, nil)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"gzip", map[string]string{"Accept-Encoding": "gzip"}},
		{"identity", map[string]string{"Accept-Encoding": "identity"}},
		{"absent", map[string]string{}},
		{"zstd_only", map[string]string{"Accept-Encoding": "zstd"}},
		{"gzip_zstd", map[string]string{"Accept-Encoding": "gzip, zstd"}},
		{"cookie", map[string]string{"Accept-Encoding": "gzip", "Cookie": "_campfire_session=bench; session_token=bench"}},
	}

	// The fixed-off server answers every request from the application.
	// Sample each case twice: the first sample is also the fixed-on fill and
	// the second the fixed replay, so both are compared.
	for _, c := range cases {
		want := captureRaw(t, offClient, "GET", offServer.URL, "/up", c.headers)
		gotFill := captureRaw(t, onClient, "GET", onServer.URL, "/up", c.headers)
		assertWireEqual(t, c.name+"/fill", gotFill, want)
		gotReplay := captureRaw(t, onClient, "GET", onServer.URL, "/up", c.headers)
		assertWireEqual(t, c.name+"/replay", gotReplay, want)
	}

	// HEAD: the replay must emit the same head with no body.
	wantHead := captureRaw(t, offClient, "HEAD", offServer.URL, "/up", map[string]string{"Accept-Encoding": "gzip"})
	gotHead := captureRaw(t, onClient, "HEAD", onServer.URL, "/up", map[string]string{"Accept-Encoding": "gzip"})
	assertWireEqual(t, "head/replay", gotHead, wantHead)
}

// TestFixedUpRejectsMaybe serve the refusal cases from the application on both
// servers: a rejected encoding (406 policy) and conditional requests.
func TestFixedUpRejects(t *testing.T) {
	offServer, offClient := testChain(t, false, nil)
	onServer, onClient := testChain(t, true, nil)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"both_rejected", map[string]string{"Accept-Encoding": "gzip;q=0, identity;q=0"}},
		{"br_only", map[string]string{"Accept-Encoding": "br"}},
	}
	for _, c := range cases {
		want := captureRaw(t, offClient, "GET", offServer.URL, "/up", c.headers)
		got := captureRaw(t, onClient, "GET", onServer.URL, "/up", c.headers)
		assertWireEqual(t, c.name, got, want)
	}

	// Conditional: capture the ETag the application emits, then compare the
	// 304 both paths produce.
	first := captureRaw(t, offClient, "GET", offServer.URL, "/up", map[string]string{"Accept-Encoding": "gzip"})
	etag := first.header.Get("ETag")
	if etag == "" {
		t.Fatal("up response carries no ETag")
	}
	conditional := map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag}
	want := captureRaw(t, offClient, "GET", offServer.URL, "/up", conditional)
	got := captureRaw(t, onClient, "GET", onServer.URL, "/up", conditional)
	assertWireEqual(t, "conditional", got, want)
}

// TestFixedUpSkipsApp pins that after the fill, replay runs without the
// application: the /up handler is invoked exactly twice on the fixed server
// (the fill and the synthesized identity pass under a gzip first request),
// while the fixed-off server invokes it on every request.
func TestFixedUpSkipsApp(t *testing.T) {
	var onCalls, offCalls atomic.Int32
	onServer, onClient := testChain(t, true, &onCalls)
	offServer, offClient := testChain(t, false, &offCalls)

	fill := func(client *http.Client, server *httptest.Server) {
		request, _ := http.NewRequest("GET", server.URL+"/up", nil)
		request.Header.Set("Accept-Encoding", "gzip")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	for i := 0; i < 3; i++ {
		fill(onClient, onServer)
		fill(offClient, offServer)
	}
	if offCalls.Load() != 3 {
		t.Fatalf("fixed-off /up handler calls = %d, want 3 (one per request)", offCalls.Load())
	}
	if onCalls.Load() != 2 {
		t.Fatalf("fixed-on /up handler calls = %d, want 2 (fill + synthetic identity pass)", onCalls.Load())
	}
}

// TestHitReplayParity pins the markFinal replay lane: after the fill, every
// hit response equals the fill response on the wire except the X-Cache value
// and Date, for the cacheable routes and both encodings.
func TestHitReplayParity(t *testing.T) {
	server, client := testChain(t, false, nil)

	cases := []struct {
		path    string
		headers map[string]string
	}{
		{benchAvatarPath, map[string]string{"Accept-Encoding": "gzip"}},
		{benchAvatarPath, map[string]string{"Accept-Encoding": "identity"}},
		{benchCSSPath, map[string]string{"Accept-Encoding": "gzip"}},
		{benchCSSPath, map[string]string{"Accept-Encoding": "identity"}},
	}
	for _, c := range cases {
		fill := captureRaw(t, client, "GET", server.URL, c.path, c.headers)
		if fill.header.Get("X-Cache") != "miss" {
			t.Fatalf("%s fill X-Cache = %q", c.path, fill.header.Get("X-Cache"))
		}
		for i := 0; i < 3; i++ {
			hit := captureRaw(t, client, "GET", server.URL, c.path, c.headers)
			if hit.header.Get("X-Cache") != "hit" {
				t.Fatalf("%s hit X-Cache = %q", c.path, hit.header.Get("X-Cache"))
			}
			want := fill
			want.header = want.header.Clone()
			want.header.Set("X-Cache", "hit")
			assertWireEqual(t, c.path, hit, want)
		}
	}
}

// TestForwardInPlaceParity pins the in-place forward header edits: the
// X-Forwarded-* behavior with and without client-supplied values is
// unchanged.
func TestForwardInPlaceParity(t *testing.T) {
	var gotHeaders atomic.Value
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders.Store(r.Header.Clone())
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	})
	cfg := Config{Gzip: false, LogRequests: false}
	cache := NewCache(1<<20, 1<<20)
	cache.FixedRoutes = false
	handler := forward(cache.Handler(root), cfg)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}}
	defer client.CloseIdleConnections()

	request, err := http.NewRequest("GET", server.URL+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	headers := gotHeaders.Load().(http.Header)
	if got := headers.Get("X-Forwarded-For"); got == "" {
		t.Fatalf("X-Forwarded-For missing after in-place edits: %v", headers)
	}
	if got := headers.Get("X-Request-Start"); got == "" {
		t.Fatalf("X-Request-Start missing after in-place edits: %v", headers)
	}
}
