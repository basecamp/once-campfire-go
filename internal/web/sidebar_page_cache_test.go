package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// TestSidebarPageCache pins the whole-page sidebar cache: warm requests serve
// byte-identical documents (identity and gzip alike), conditional requests
// return 304 against the stored validator, Turbo-Frame requests never serve
// the document bytes, a session flash always renders fresh and never poisons
// the cache, and an audited account write moves the key so the served page
// picks the change up.
func TestSidebarPageCache(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	get := func(extra func(*http.Request)) (*http.Response, []byte) {
		t.Helper()
		r, err := http.NewRequest("GET", server.URL+"/users/me/sidebar", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
		if extra != nil {
			extra(r)
		}
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, body
	}
	decode := func(wire []byte) []byte {
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
	res1, first := get(nil)
	if first == nil || !bytes.Contains(first, []byte(`<!DOCTYPE html>`)) || !bytes.Contains(first, []byte(`</html>`)) {
		t.Fatal("first document render is incomplete")
	}
	etag := res1.Header.Get("ETag")
	if etag == "" {
		t.Fatal("document missing weak validator")
	}
	_, second := get(nil)
	if !bytes.Equal(first, second) {
		t.Fatal("warm document differs from the first render")
	}
	resGzip, wire := get(func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if resGzip.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip request served %q", resGzip.Header.Get("Content-Encoding"))
	}
	if !bytes.Equal(first, decode(wire)) {
		t.Fatal("gzip member decodes to different bytes than the identity document")
	}
	if resGzip.Header.Get("ETag") != etag {
		t.Fatal("gzip serve changed the validator")
	}
	res304, body304 := get(func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
	if res304.StatusCode != http.StatusNotModified || len(body304) != 0 {
		t.Fatalf("conditional request: %d %q", res304.StatusCode, body304)
	}
	_, turbo := get(func(r *http.Request) { r.Header.Set("Turbo-Frame", "user_sidebar") })
	if bytes.Equal(first, turbo) {
		t.Fatal("Turbo-Frame request served the document bytes")
	}
	if !bytes.Contains(turbo, []byte(`<turbo-frame id="user_sidebar"`)) {
		t.Fatal("Turbo-Frame response lost the frame")
	}
	// A session flash renders fresh (never the cached page) and is not stored.
	flashRaw, err := app.Secrets.EncryptCookie(browserSessionCookie,
		map[string]any{"session_id": "flash-test", "flash": map[string]any{"discard": []any{}, "flashes": map[string]any{"notice": "hello flash"}}},
		time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	flashCookie := &http.Cookie{Name: browserSessionCookie, Value: rails.EscapeCookie(flashRaw), Path: "/"}
	_, flashed := get(func(r *http.Request) { r.AddCookie(flashCookie) })
	if !bytes.Contains(flashed, []byte("hello flash")) {
		t.Fatal("flash-carrying request did not render fresh")
	}
	_, afterFlash := get(nil)
	if !bytes.Equal(first, afterFlash) {
		t.Fatal("flash render poisoned the cached page")
	}
	// An audited account write moves the gate version: the next request
	// renders the new page and refills the cache.
	styles := "body{--page-cache:1}"
	if err := app.DB.UpdateAccount(context.Background(), nil, &styles, nil, false); err != nil {
		t.Fatal(err)
	}
	_, changed := get(nil)
	if !bytes.Contains(changed, []byte(styles)) {
		t.Fatal("audited write did not invalidate the cached page")
	}
	_, changed2 := get(nil)
	if !bytes.Equal(changed, changed2) {
		t.Fatal("refilled page not byte-stable")
	}
}

// TestSidebarPageCacheStaysFreshForUserRowPins the un-audited user-row case:
// the whole page is keyed on the session user's rendered markers, so a direct
// users write (which does not bump the gate version) still changes the served
// document instead of replaying the stale page.
func TestSidebarPageCacheStaysFreshForUserRow(t *testing.T) {
	app, server, cookie, user := testApp(t)
	get := func() string {
		t.Helper()
		r, err := http.NewRequest("GET", server.URL+"/users/me/sidebar", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	first := get()
	if _, err := app.DB.Write.ExecContext(context.Background(), "UPDATE users SET name=? WHERE id=?", "Fresh Profile Name", user.ID); err != nil {
		t.Fatal(err)
	}
	if second := get(); !strings.Contains(second, "Fresh Profile Name") {
		t.Fatal("direct user write stayed hidden by the cached page")
	}
	_ = first
}

// TestSidebarPageCacheRequestScopedUnit pins the key derivation and the
// storage accounting without the HTTP pipeline.
func TestSidebarPageCacheKeyShape(t *testing.T) {
	u := databaseUserFixture()
	key := sidebarPageKey(7, u, false)
	if !strings.HasPrefix(key, "sidebar-page/7/") || strings.Contains(key, "/frame") {
		t.Fatalf("document key shape: %q", key)
	}
	frameKey := sidebarPageKey(7, u, true)
	if !strings.HasSuffix(frameKey, "/frame") {
		t.Fatalf("frame key shape: %q", frameKey)
	}
	if key == sidebarPageKey(8, u, false) {
		t.Fatal("version not part of the key")
	}
	if key == sidebarPageKey(7, database.User{}, false) {
		t.Fatal("user markers not part of the key")
	}
}

// databaseUserFixture builds a user row the sidebars' key uses.
func databaseUserFixture() database.User {
	return database.User{ID: 3, Name: "fixture", Role: 1}
}
