package web

// Full-stack differential for the front fixed-route table: the /up response
// with the application and with the fixed replay must be byte-identical
// across the encodings, Accept formats and methods the official harness and
// browsers send. The front chain here is the production public chain (minus
// forward, which only edits request headers and is response-neutral).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/engine"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

type wireExchange struct {
	status int
	header http.Header
	body   []byte
}

func fetch(t *testing.T, client *http.Client, url, method, path string, headers map[string]string) wireExchange {
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
	return wireExchange{status: response.StatusCode, header: header, body: body}
}

func assertExchange(t *testing.T, name string, got, want wireExchange) {
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

// parityApp builds one application and returns the two public chains.
func parityApp(t *testing.T) (app *Server, offHandler, onHandler http.Handler) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("recorded-pieces")
	if err != nil {
		t.Fatal(err)
	}
	app, err = New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if _, err = db.Setup(context.Background(), "Owner", "owner@test", "digest"); err != nil {
		t.Fatal(err)
	}
	cfg := front.Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	rootHandler := engine.New(front.Deflate(app), engine.Config{Mode: engine.ModeOn})
	off := front.NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	off.FixedRoutes = false
	on := front.NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	on.FixedRoutes = true
	return app, front.PublicCompression(off.Handler(rootHandler), cfg), front.PublicCompression(on.Handler(rootHandler), cfg)
}

// TestFixedUpRealAppParity compares the fixed /up replay against the real
// application across Accept formats, encodings and methods.
func TestFixedUpRealAppParity(t *testing.T) {
	_, offHandler, onHandler := parityApp(t)
	offServer := httptest.NewServer(offHandler)
	defer offServer.Close()
	onServer := httptest.NewServer(onHandler)
	defer onServer.Close()
	newClient := func(server *httptest.Server) *http.Client {
		client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}}
		t.Cleanup(client.CloseIdleConnections)
		return client
	}
	offClient, onClient := newClient(offServer), newClient(onServer)

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"no_accept_gzip", map[string]string{"Accept-Encoding": "gzip"}},
		{"no_accept_identity", map[string]string{"Accept-Encoding": "identity"}},
		{"json_accept", map[string]string{"Accept-Encoding": "gzip", "Accept": "application/json"}},
		{"text_html_accept", map[string]string{"Accept-Encoding": "gzip", "Accept": "text/html"}},
		{"browser_accept", map[string]string{"Accept-Encoding": "gzip", "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}},
		{"cookie", map[string]string{"Accept-Encoding": "gzip", "Cookie": "_campfire_session=bench"}},
	}
	for _, c := range cases {
		want := fetch(t, offClient, offServer.URL, "GET", "/up", c.headers)
		gotFill := fetch(t, onClient, onServer.URL, "GET", "/up", c.headers)
		assertExchange(t, c.name+"/fill", gotFill, want)
		gotReplay := fetch(t, onClient, onServer.URL, "GET", "/up", c.headers)
		assertExchange(t, c.name+"/replay", gotReplay, want)
	}

	// HEAD under gzip: same head, no body, via the app on both chains.
	wantHead := fetch(t, offClient, offServer.URL, "HEAD", "/up", map[string]string{"Accept-Encoding": "gzip"})
	gotHead := fetch(t, onClient, onServer.URL, "HEAD", "/up", map[string]string{"Accept-Encoding": "gzip"})
	assertExchange(t, "head", gotHead, wantHead)

	// Rejected encodings and conditionals stay on the application path.
	reject := map[string]string{"Accept-Encoding": "gzip;q=0, identity;q=0"}
	want := fetch(t, offClient, offServer.URL, "GET", "/up", reject)
	got := fetch(t, onClient, onServer.URL, "GET", "/up", reject)
	assertExchange(t, "rejected_encoding", got, want)

	first := fetch(t, offClient, offServer.URL, "GET", "/up", map[string]string{"Accept-Encoding": "gzip"})
	conditional := map[string]string{"Accept-Encoding": "gzip", "If-None-Match": first.header.Get("ETag")}
	want = fetch(t, offClient, offServer.URL, "GET", "/up", conditional)
	got = fetch(t, onClient, onServer.URL, "GET", "/up", conditional)
	assertExchange(t, "conditional", got, want)
}

// TestFixedUpRealAppSkipsApp pins that replay skips the application: after
// the fill, every additional request serves from the table.
func TestFixedUpRealAppSkipsApp(t *testing.T) {
	var calls atomic.Int32
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("recorded-pieces")
	if err != nil {
		t.Fatal(err)
	}
	serverApp, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(serverApp.Close)
	if _, err = db.Setup(context.Background(), "Owner", "owner@test", "digest"); err != nil {
		t.Fatal(err)
	}
	counting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		serverApp.ServeHTTP(w, r)
	})
	cfg := front.Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: 64 << 20, MaxCacheItemSize: 1 << 20}
	rootHandler := engine.New(front.Deflate(counting), engine.Config{Mode: engine.ModeOn})
	cache := front.NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = true
	handler := front.PublicCompression(cache.Handler(rootHandler), cfg)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}}
	defer client.CloseIdleConnections()
	for i := 0; i < 3; i++ {
		response, err := client.Get(server.URL + "/up")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || len(body) == 0 {
			t.Fatalf("up status %d body %d", response.StatusCode, len(body))
		}
	}
	// Fill (one app pass) + the synthetic identity pass = 2; replays serve 0.
	if calls.Load() != 2 {
		t.Fatalf("app invoked %d times, want 2", calls.Load())
	}
}
