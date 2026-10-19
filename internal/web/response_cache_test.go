package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// testResponseCacheApp builds the app the response-cache tests drive, with
// real time: the recorded-piece caches key the message window on the room's
// updated_at, so a message write must actually move that stamp to invalidate
// (frozen time would freeze the key with it).
func testResponseCacheApp(t *testing.T) (*Server, *httptest.Server, *http.Cookie, database.User) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("response-cache")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(context.Background(), user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(front.Deflate(app))
	t.Cleanup(server.Close)
	return app, server, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user
}

// TestResponseCacheWarmHits pins the whole-response cache on the three read
// routes: after the first (filling) render, warm requests serve byte-identical
// bodies — identity and gzip alike — with the same entity headers, and the
// cache counters move. A conditional request against the stored validator gets
// the same 304 a fresh render would answer.
func TestResponseCacheWarmHits(t *testing.T) {
	app, server, cookie, user := testResponseCacheApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	for i := 0; i < 3; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "", "<p>response cache seed</p>", "response cache seed"); err != nil {
			t.Fatal(err)
		}
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	hits0, _ := app.responses.counters()
	type cell struct {
		label, path, accept string
	}
	roomPath := "/rooms/" + strconv.FormatInt(room.ID, 10)
	cells := []cell{
		{"room gzip", roomPath, "gzip"},
		{"room identity", roomPath, "identity"},
		{"messages gzip", roomPath + "/messages", "gzip"},
		{"messages identity", roomPath + "/messages", "identity"},
		{"search gzip", "/searches?q=response", "gzip"},
		{"search identity", "/searches?q=response", "identity"},
	}
	first := map[string]recordedExchange{}
	for _, c := range cells {
		exchange := recordedGet(t, client, server, c.path, c.accept, "", cookie)
		if exchange.status != 200 || len(exchange.body) == 0 {
			t.Fatalf("%s: fill returned %d with %d bytes", c.label, exchange.status, len(exchange.body))
		}
		if exchange.contentType != "text/html; charset=utf-8" {
			t.Fatalf("%s: content type %q", c.label, exchange.contentType)
		}
		if exchange.etag == "" {
			t.Fatalf("%s: missing validator", c.label)
		}
		first[c.label] = exchange
	}
	// Every warm request must be byte-identical to the fill, in its own
	// encoding, with the same validator, and the cache must count the hits.
	for _, c := range cells {
		exchange := recordedGet(t, client, server, c.path, c.accept, "", cookie)
		want := first[c.label]
		if !bytes.Equal(exchange.body, want.body) {
			t.Fatalf("%s: warm body differs from the fill (%d vs %d bytes)", c.label, len(exchange.body), len(want.body))
		}
		if exchange.etag != want.etag {
			t.Fatalf("%s: warm ETag %q vs fill %q", c.label, exchange.etag, want.etag)
		}
		if exchange.encoding != want.encoding {
			t.Fatalf("%s: warm encoding %q vs fill %q", c.label, exchange.encoding, want.encoding)
		}
		if exchange.cacheControl != want.cacheControl {
			t.Fatalf("%s: warm Cache-Control %q vs fill %q", c.label, exchange.cacheControl, want.cacheControl)
		}
		if !strings.Contains(exchange.vary, "Accept-Encoding") {
			t.Fatalf("%s: warm Vary %q lacks Accept-Encoding", c.label, exchange.vary)
		}
	}
	hits1, _ := app.responses.counters()
	if hits1 <= hits0 {
		t.Fatalf("warm pass produced no cache hits (hits %d -> %d)", hits0, hits1)
	}
	// The gzip members must decode to the identity page.
	identity := first["room identity"].body
	if !bytes.Equal(identity, decodeGzip(t, first["room gzip"].body)) {
		t.Fatal("room gzip member decodes to different bytes than the identity page")
	}
	// A conditional request against the stored validator returns 304.
	res304 := recordedGet(t, client, server, roomPath, "gzip", first["room gzip"].etag, cookie)
	if res304.status != http.StatusNotModified || len(res304.body) != 0 {
		t.Fatalf("conditional hit: %d %d bytes", res304.status, len(res304.body))
	}
}

// TestResponseCacheInvalidation: any committed write moves the observed
// generation, so the next request re-renders and refills; the stale entry is
// never served. A HEAD request against a warm entry reports the GET body
// length without emitting it.
func TestResponseCacheInvalidation(t *testing.T) {
	app, server, cookie, user := testResponseCacheApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "", "<p>before</p>", "before"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := "/rooms/" + strconv.FormatInt(room.ID, 10)
	first := recordedGet(t, client, server, path, "gzip", "", cookie)
	if !bytes.Contains(decodeGzip(t, first.body), []byte("before")) {
		t.Fatal("fill render does not contain the seeded message")
	}
	// A warm hit serves the stored page.
	warm := recordedGet(t, client, server, path, "gzip", "", cookie)
	if !bytes.Equal(warm.body, first.body) {
		t.Fatal("warm body differs from the fill")
	}
	// The write bumps the observed generation; the next request re-renders
	// with the new message and the stale entry is gone.
	if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "", "<p>after</p>", "after"); err != nil {
		t.Fatal(err)
	}
	refilled := recordedGet(t, client, server, path, "gzip", "", cookie)
	decoded := decodeGzip(t, refilled.body)
	if bytes.Equal(refilled.body, first.body) || !bytes.Contains(decoded, []byte("after")) {
		t.Fatal("committed write did not invalidate the cached page")
	}
	// And the refilled page serves warm again.
	again := recordedGet(t, client, server, path, "gzip", "", cookie)
	if !bytes.Equal(again.body, refilled.body) {
		t.Fatal("refill not served warm")
	}
	// A HEAD request against a warm entry reports the GET body length with no
	// body, exactly like the fresh HEAD path.
	head := recordedHead(t, client, server, path, "gzip", cookie)
	if head.status != 200 || len(head.body) != 0 || head.contentLength != strconv.Itoa(len(refilled.body)) {
		t.Fatalf("HEAD on warm entry: %d, %d body bytes, Content-Length %q vs %d", head.status, len(head.body), head.contentLength, len(refilled.body))
	}
}

// TestResponseCacheFlashBypass: a request carrying a session flash renders
// fresh (and is never stored), so a flash cannot poison the cached page.
func TestResponseCacheFlashBypass(t *testing.T) {
	app, server, cookie, user := testResponseCacheApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/rooms/" + strconv.FormatInt(rooms[0].ID, 10)
	client := recordedClient()
	defer client.CloseIdleConnections()
	plain := recordedGet(t, client, server, path, "gzip", "", cookie)
	flashRaw, err := app.Secrets.EncryptCookie(browserSessionCookie,
		map[string]any{"session_id": "flash-test", "flash": map[string]any{"discard": []any{}, "flashes": map[string]any{"notice": "hello flash"}}},
		time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	flashCookie := &http.Cookie{Name: browserSessionCookie, Value: rails.EscapeCookie(flashRaw), Path: "/"}
	flashed := recordedFetchWithExtra(t, client, server, path, "gzip", cookie, flashCookie)
	if !bytes.Contains(decodeGzip(t, flashed.body), []byte("hello flash")) {
		t.Fatal("flash-carrying request did not render fresh")
	}
	after := recordedGet(t, client, server, path, "gzip", "", cookie)
	if !bytes.Equal(after.body, plain.body) {
		t.Fatal("flash render poisoned the cached page")
	}
}

// TestResponseCacheDisabledIsByteIdentical: a zero budget keeps every lookup a
// miss (nothing is stored), and repeated renders stay byte-identical — the
// rollback switch changes only speed, never bytes.
func TestResponseCacheDisabledIsByteIdentical(t *testing.T) {
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	app, server, cookie, user := testResponseCacheApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "", "<p>uncached</p>", "uncached"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := "/rooms/" + strconv.FormatInt(rooms[0].ID, 10)
	first := recordedGet(t, client, server, path, "gzip", "", cookie)
	second := recordedGet(t, client, server, path, "gzip", "", cookie)
	if !bytes.Equal(first.body, second.body) {
		t.Fatal("repeated renders differ with the response cache off")
	}
	hits, _ := app.responses.counters()
	if hits != 0 {
		t.Fatalf("budget-zero cache reported %d hits", hits)
	}
}

// TestResponseCacheFramedPath drives the cache through the production writer
// chain (fastserve's precomposed receiver, arena body, head block): a warm
// messages-page request must serve from the response cache with a raw HTTP
// exchange byte-identical to the fresh render's — status line and headers
// included. The messages route carries no Set-Cookie, so it takes the framed
// path; the room route's last_room cookie always falls back to the map path.
func TestResponseCacheFramedPath(t *testing.T) {
	app, _, cookie, user := testResponseCacheApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "", "<p>framed seed</p>", "framed seed"); err != nil {
		t.Fatal(err)
	}
	_, addr := serveLoop(t, front.Deflate(app))
	path := "/rooms/" + strconv.FormatInt(rooms[0].ID, 10) + "/messages"
	first, firstStatus := rawFetch(t, addr, "campfire.test", "GET", path, "gzip", "", cookie)
	if firstStatus != 200 {
		t.Fatalf("fresh framed render: %d", firstStatus)
	}
	hits, _ := app.responses.counters()
	second, secondStatus := rawFetch(t, addr, "campfire.test", "GET", path, "gzip", "", cookie)
	if secondStatus != 200 {
		t.Fatalf("warm framed render: %d", secondStatus)
	}
	hits2, _ := app.responses.counters()
	if hits2 <= hits {
		t.Fatalf("framed warm request did not hit the cache (hits %d -> %d)", hits, hits2)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("framed warm exchange differs from the fresh render:\n%s\nvs\n%s", first, second)
	}
}

func decodeGzip(t *testing.T, wire []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// recordedFetchWithExtra sends one request with the session cookie plus an
// additional cookie (a session flash), the shape of a real browser.
func recordedFetchWithExtra(t *testing.T, client *http.Client, server *httptest.Server, path, accept string, cookie, extra *http.Cookie) recordedExchange {
	t.Helper()
	request, err := http.NewRequest("GET", server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "campfire.test"
	if accept != "" {
		request.Header.Set("Accept-Encoding", accept)
	}
	request.AddCookie(cookie)
	request.AddCookie(extra)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return recordedExchange{
		status:        response.StatusCode,
		etag:          response.Header.Get("ETag"),
		encoding:      response.Header.Get("Content-Encoding"),
		vary:          response.Header.Get("Vary"),
		contentType:   response.Header.Get("Content-Type"),
		contentLength: response.Header.Get("Content-Length"),
		cacheControl:  response.Header.Get("Cache-Control"),
		body:          body,
	}
}

// testResponseCachePair builds two Server instances over one database — the
// whole-response cache on (the default budget) and off (a zero budget) — so
// the same request can be compared byte for byte and the on-side hit counter
// can prove engagement. The frozen clock keeps the render deterministic.
func testResponseCachePair(t *testing.T) (*Server, *Server, *httptest.Server, *httptest.Server, *http.Cookie, database.User) {
	t.Helper()
	on, off, cookie, user := testResponseCacheApps(t)
	onServer := httptest.NewServer(front.Deflate(on))
	t.Cleanup(onServer.Close)
	offServer := httptest.NewServer(front.Deflate(off))
	t.Cleanup(offServer.Close)
	return on, off, onServer, offServer, cookie, user
}

// testResponseCacheApps is testResponseCachePair without the httptest servers:
// the replay tests wrap the two apps in their own front chain and serve them
// through the owned loop, where the raw wire bytes can be compared verbatim.
func testResponseCacheApps(t *testing.T) (*Server, *Server, *http.Cookie, database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("response-cache-pair")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "")
	on, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	off, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "")
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	return on, off, newSessionCookie(t, secrets, db, user.ID), user
}

// compareEntityExchange requires the cache-on and cache-off exchanges to be
// byte-identical, including the entity validator and cache policy.
func compareEntityExchange(t *testing.T, label string, on, off recordedExchange) {
	t.Helper()
	if on.status != off.status {
		t.Fatalf("%s: status %d != %d", label, on.status, off.status)
	}
	if !bytes.Equal(on.body, off.body) {
		t.Fatalf("%s: bodies differ (%d vs %d bytes)", label, len(on.body), len(off.body))
	}
	if on.etag != off.etag {
		t.Fatalf("%s: ETag %q != %q", label, on.etag, off.etag)
	}
	if on.cacheControl != off.cacheControl {
		t.Fatalf("%s: Cache-Control %q != %q", label, on.cacheControl, off.cacheControl)
	}
	if on.encoding != off.encoding {
		t.Fatalf("%s: encoding %q != %q", label, on.encoding, off.encoding)
	}
	if on.contentType != off.contentType {
		t.Fatalf("%s: content type %q != %q", label, on.contentType, off.contentType)
	}
	if on.contentLength != off.contentLength {
		t.Fatalf("%s: content length %q != %q", label, on.contentLength, off.contentLength)
	}
}

// TestResponseCacheEmptySearchValidators pins the empty-result search shape:
// the recorded piece path does not run (no matching messages), so the ETag and
// Cache-Control come from finish's body fallback. Admission must see those
// finalized values, and a warm hit must reproduce them — bytes, validator and
// policy — and answer a conditional request with 304. Every case asserts cache
// engagement, so a request that never touches the round cannot pass.
func TestResponseCacheEmptySearchValidators(t *testing.T) {
	on, _, onServer, offServer, cookie, _ := testResponseCachePair(t)
	client := recordedClient()
	defer client.CloseIdleConnections()
	const path = "/searches?q=no-such-term-at-all"

	for _, c := range []struct {
		label, accept string
		engages       bool
	}{
		{"identity", "identity", true},
		// A non-recorded gzip body is written identity and coded by the front
		// layer, so the store gate (the stored coding must be the one the key
		// names) refuses it: the case pins parity and non-engagement.
		{"gzip", "gzip", false},
	} {
		t.Run(c.label, func(t *testing.T) {
			hits0, _ := on.responses.counters()
			fillOn := recordedGet(t, client, onServer, path, c.accept, "", cookie)
			fillOff := recordedGet(t, client, offServer, path, c.accept, "", cookie)
			if fillOn.status != 200 || fillOff.status != 200 {
				t.Fatalf("fill: on %d off %d", fillOn.status, fillOff.status)
			}
			compareEntityExchange(t, "fill", fillOn, fillOff)
			if fillOn.etag == "" || fillOn.cacheControl != "max-age=0, private, must-revalidate" {
				t.Fatalf("fill validator %q cache-control %q", fillOn.etag, fillOn.cacheControl)
			}
			// Warm: the on server must serve the stored page with the same
			// validator, policy and bytes as the fill.
			warmOn := recordedGet(t, client, onServer, path, c.accept, "", cookie)
			warmOff := recordedGet(t, client, offServer, path, c.accept, "", cookie)
			compareEntityExchange(t, "warm", warmOn, warmOff)
			if !bytes.Equal(warmOn.body, fillOn.body) {
				t.Fatalf("warm body differs from the fill (%d vs %d bytes)", len(warmOn.body), len(fillOn.body))
			}
			if warmOn.etag != fillOn.etag || warmOn.cacheControl != fillOn.cacheControl {
				t.Fatalf("warm validators differ: ETag %q vs %q, Cache-Control %q vs %q", warmOn.etag, fillOn.etag, warmOn.cacheControl, fillOn.cacheControl)
			}
			// A conditional request against the fill validator must 304.
			condOn := recordedGet(t, client, onServer, path, c.accept, fillOn.etag, cookie)
			condOff := recordedGet(t, client, offServer, path, c.accept, fillOff.etag, cookie)
			if condOn.status != http.StatusNotModified || condOff.status != http.StatusNotModified {
				t.Fatalf("conditional: on %d off %d", condOn.status, condOff.status)
			}
			if len(condOn.body) != 0 || len(condOff.body) != 0 {
				t.Fatalf("conditional body: on %d off %d bytes", len(condOn.body), len(condOff.body))
			}
			// HEAD reports the GET body length with the same validator and no
			// body.
			headOn := recordedHead(t, client, onServer, path, c.accept, cookie)
			headOff := recordedHead(t, client, offServer, path, c.accept, cookie)
			if headOn.status != 200 || headOff.status != 200 {
				t.Fatalf("head: on %d off %d", headOn.status, headOff.status)
			}
			if headOn.contentLength != headOff.contentLength || headOn.contentLength != fillOn.contentLength {
				t.Fatalf("head content-length: on %q off %q fill %q", headOn.contentLength, headOff.contentLength, fillOn.contentLength)
			}
			if headOn.etag != headOff.etag || headOn.cacheControl != headOff.cacheControl {
				t.Fatalf("head validators: on %q/%q off %q/%q", headOn.etag, headOn.cacheControl, headOff.etag, headOff.cacheControl)
			}
			if len(headOn.body) != 0 || len(headOff.body) != 0 {
				t.Fatalf("head body: on %d off %d bytes", len(headOn.body), len(headOff.body))
			}
			hits1, misses1 := on.responses.counters()
			if c.engages {
				if hits1 <= hits0 {
					t.Fatalf("warm %s request did not hit the response cache (hits %d -> %d, misses %d)", c.label, hits0, hits1, misses1)
				}
			} else if hits1 != hits0 {
				t.Fatalf("non-engaging %s request moved the hit counter (%d -> %d)", c.label, hits0, hits1)
			}
		})
	}
}
