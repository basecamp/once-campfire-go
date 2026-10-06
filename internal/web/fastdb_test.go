package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestParseFastDB(t *testing.T) {
	for _, c := range []struct {
		value          string
		enabled, valid bool
	}{
		{"", true, true},
		{"on", true, true},
		{"true", true, true},
		{"1", true, true},
		{"off", false, true},
		{"false", false, true},
		{"0", false, true},
		{"OFF", false, true},
		{"nope", true, false},
	} {
		if enabled, valid := parseFastDB(c.value); enabled != c.enabled || valid != c.valid {
			t.Errorf("parseFastDB(%q) = (%v, %v), want (%v, %v)", c.value, enabled, valid, c.enabled, c.valid)
		}
	}
}

// testFastPair serves two web.Server instances over one database — the fast
// read path (CAMPFIRE_FASTDB default on) and the database/sql fallback
// (CAMPFIRE_FASTDB=off) — so the same request can be compared byte for byte.
// Time is frozen so every timestamp in the pages and cookies is
// deterministic.
func testFastPair(t *testing.T) (on, off *Server, onServer, offServer *httptest.Server, cookie *http.Cookie, user database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("fastdb-parity")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, err = db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := db.CreateUser(ctx, "Alice", "alice@test", "digest", "a bio", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("setup rooms: %v %d", err, len(rooms))
	}
	open := rooms[0].ID
	for i := 0; i < 5; i++ {
		if _, err := db.CreateMessage(ctx, user.ID, open, "", fmt.Sprintf("<p>hello %d</p>", i), fmt.Sprintf("hello %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateRoom(ctx, user.ID, "Rooms::Direct", "", []int64{alice.ID}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CAMPFIRE_FASTDB", "on")
	on, err = New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_FASTDB", "off")
	off, err = New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	t.Setenv("CAMPFIRE_FASTDB", "")
	if on.fastdb == nil {
		t.Fatal("fast server has no fast read pool")
	}
	if off.fastdb != nil {
		t.Fatal("off server has a fast read pool")
	}
	onServer = httptest.NewServer(on)
	t.Cleanup(onServer.Close)
	offServer = httptest.NewServer(off)
	t.Cleanup(offServer.Close)
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return on, off, onServer, offServer, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user
}

// parityGet runs one GET against a server with a pinned Host header so both
// sides render the same origin into their bodies, and returns status and body.
func parityGet(t *testing.T, server *httptest.Server, path string, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest("GET", server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "chat.test"
	request.Header.Set("Accept", "*/*")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

// TestFastDBReadParity runs the hot read routes through both servers — the
// fast path and the database/sql path — and requires identical status, body
// bytes, content type and etag. The requests cover room show (plain and at
// an anchor, so the reduced ref path and the full-message around path both
// run), the messages page (before, after and empty-page), the session lookup
// inside auth (every request goes through it), the sidebar (SidebarRooms,
// per-direct RoomMembers and DirectPlaceholders) and the search FTS scan
// (hits and an empty result).
func TestFastDBReadParity(t *testing.T) {
	_, _, onServer, offServer, cookie, user := testFastPair(t)
	for i, path := range []string{
		"/rooms/1",
		"/rooms/1/@1",
		"/rooms/1/messages",
		"/rooms/1/messages?before=1",
		"/rooms/1/messages?after=2",
		fmt.Sprintf("/users/%d/sidebar", user.ID),
		"/users/me/sidebar",
		"/searches?q=hello",
		"/searches?q=absent-term",
	} {
		fast, body := parityGet(t, onServer, path, cookie)
		slow, legacy := parityGet(t, offServer, path, cookie)
		if fast.StatusCode != slow.StatusCode {
			t.Errorf("%d %s: fast %d != legacy %d", i, path, fast.StatusCode, slow.StatusCode)
			continue
		}
		if fast.Header.Get("Content-Type") != slow.Header.Get("Content-Type") {
			t.Errorf("%d %s: content type %q != %q", i, path, fast.Header.Get("Content-Type"), slow.Header.Get("Content-Type"))
		}
		if fast.Header.Get("ETag") != slow.Header.Get("ETag") {
			t.Errorf("%d %s: etag %q != %q", i, path, fast.Header.Get("ETag"), slow.Header.Get("ETag"))
		}
		if string(body) != string(legacy) {
			t.Errorf("%d %s: fast body (%d bytes) != legacy body (%d bytes)", i, path, len(body), len(legacy))
			continue
		}
	}
}

// TestFastDBFallbackOnPoolFailure pins the downgrade path: a server whose
// fast read pool cannot open (the pool path does not hold a database) still
// constructs and serves through database/sql.
func TestFastDBFallbackOnPoolFailure(t *testing.T) {
	t.Setenv("CAMPFIRE_FASTDB", "on")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, err := rails.NewSecrets("downgrade")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, filepath.Join(root, "missing.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.fastdb != nil {
		t.Fatal("expected the fast pool to be absent after a failed open")
	}
}
