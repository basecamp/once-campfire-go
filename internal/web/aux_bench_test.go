package web

// Benchmarks for the app-side cost of the auxiliary routes: /up runs the full
// web server path on every request (it is not cacheable), so its per-request
// cost is the sum of the front chain (internal/front benchmarks) plus what is
// measured here: engine fallback, Deflate, the arena/response-buffer/session
// machinery and the health handler. avatar/static_css only pay this on the
// first (cache-fill) request; the fill cost is measured for completeness.

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/engine"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// benchApp builds a full application over a fresh database mirroring
// testSingleApp's wiring (frozen clock, engine wrapped around Deflate), with
// a signed session cookie for the authenticated routes.
func benchApp(b *testing.B) (*Server, *httptest.Server, string) {
	b.Helper()
	b.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := b.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("recorded-pieces")
	if err != nil {
		b.Fatal(err)
	}
	app, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(app.Close)
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		b.Fatal(err)
	}
	token, err := db.StartSession(context.Background(), user.ID, "test", "127.0.0.1")
	if err != nil {
		b.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		b.Fatal(err)
	}
	cookie := "session_token=" + rails.EscapeCookie(signed) + "; _campfire_session=bench"
	rootHandler := engine.New(front.Deflate(app), engine.Config{Mode: engine.ModeOn})
	server := httptest.NewServer(rootHandler)
	b.Cleanup(server.Close)
	return app, server, cookie
}

// webGet runs one keep-alive GET with the loadgen's header shape. The
// response body is returned raw (the loadgen negotiates gzip and decodes on
// its own CPUs; the benchmark measures the server, so the client's
// decompression must not ride along).
func webGet(b *testing.B, client *http.Client, url, path, cookie string) (int, string) {
	b.Helper()
	request, err := http.NewRequest("GET", url+path, nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Cookie", cookie)
	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		b.Fatal(err)
	}
	if response.StatusCode != 200 {
		b.Fatalf("%s status %d", path, response.StatusCode)
	}
	return len(body), response.Header.Get("Content-Encoding")
}

// webCheck decodes one response (outside the timed loop) and verifies the
// decoded body matches the expected bytes.
func webCheck(b *testing.B, client *http.Client, url, path, cookie string, want []byte) {
	b.Helper()
	request, err := http.NewRequest("GET", url+path, nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Cookie", cookie)
	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		b.Fatal(err)
	}
	if response.Header.Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		body, err = io.ReadAll(reader)
		if err != nil {
			b.Fatal(err)
		}
	}
	if !bytes.Equal(body, want) {
		b.Fatalf("%s body %q, want %q", path, body, want)
	}
}

func benchWebClient(b *testing.B, server *httptest.Server) *http.Client {
	b.Helper()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	return client
}

// BenchmarkWebUp measures /up through the real application: engine fallback,
// Deflate, the web server request machinery and the health handler, on every
// request (the route is uncacheable).
func BenchmarkWebUp(b *testing.B) {
	_, server, cookie := benchApp(b)
	client := benchWebClient(b, server)
	webCheck(b, client, server.URL, "/up", cookie, []byte(HealthBody))
	wireLen, _ := webGet(b, client, server.URL, "/up", cookie)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		length, _ := webGet(b, client, server.URL, "/up", cookie)
		if length != wireLen {
			b.Fatalf("up wire body %d bytes, want %d", length, wireLen)
		}
	}
}

// BenchmarkWebAvatarFill measures the full app cost of one avatar response
// (signed token verification, session auth, user read, storage probe, SVG
// generation). Only the first request pays this; hits replay from the front
// cache.
func BenchmarkWebAvatarFill(b *testing.B) {
	app, server, cookie := benchApp(b)
	client := benchWebClient(b, server)
	path := "/users/" + app.Secrets.SignedID("User", 1, "avatar", time.Time{}) + "/avatar"
	wireLen, _ := webGet(b, client, server.URL, path, cookie)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		length, _ := webGet(b, client, server.URL, path, cookie)
		if length != wireLen {
			b.Fatalf("avatar wire body %d bytes, want %d", length, wireLen)
		}
	}
}

// BenchmarkWebStaticCSSFill measures the full app cost of one static asset
// response (assets.Serve over the embedded files + response buffer ETag).
func BenchmarkWebStaticCSSFill(b *testing.B) {
	_, server, cookie := benchApp(b)
	client := benchWebClient(b, server)
	wireLen, _ := webGet(b, client, server.URL, "/assets/_reset-9c3efd7b.css", cookie)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		length, _ := webGet(b, client, server.URL, "/assets/_reset-9c3efd7b.css", cookie)
		if length != wireLen {
			b.Fatalf("css wire body %d bytes, want %d", length, wireLen)
		}
	}
}
