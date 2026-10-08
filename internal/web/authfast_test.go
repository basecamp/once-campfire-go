package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestParseAuthFast(t *testing.T) {
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
		if enabled, valid := parseAuthFast(c.value); enabled != c.enabled || valid != c.valid {
			t.Errorf("parseAuthFast(%q) = (%v, %v), want (%v, %v)", c.value, enabled, valid, c.enabled, c.valid)
		}
	}
}

// TestVerifiedCookieCache pins the cache contract behind the auth fast path:
// only a verified value is stored, a hit is refused once its signed expiry
// passes or the signing-key generation changes, removal works, the shards stay
// bounded, and concurrent access is safe.
func TestVerifiedCookieCache(t *testing.T) {
	secrets, err := rails.NewSecrets("cache-test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := rails.NewSecrets("other-secret")
	if err != nil {
		t.Fatal(err)
	}
	fp := secrets.SigningFingerprint()
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	raw, err := secrets.SignCookie("session_token", "token-1", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	cache := newVerifiedCookieCache()
	if _, ok := cache.lookup(raw, now, fp); ok {
		t.Fatal("lookup hit before any store")
	}

	// A full verification is what inserts entries.
	var token string
	expires, err := secrets.VerifyCookieExpires("session_token", raw, now, &token)
	if err != nil {
		t.Fatal(err)
	}
	cache.store(raw, token, expires, fp)
	if got, ok := cache.lookup(raw, now, fp); !ok || got != "token-1" {
		t.Fatalf("lookup = %q, %v; want token-1, true", got, ok)
	}

	// A tampered value is never stored and never served (a miss falls through
	// to the full verification, which rejects it).
	tampered := []byte(raw)
	tampered[len(tampered)/2] ^= 0x40
	if _, ok := cache.lookup(string(tampered), now, fp); ok {
		t.Fatal("tampered value served from cache")
	}
	if err := secrets.VerifyCookie("session_token", string(tampered), now, &token); err == nil {
		t.Fatal("tampered value verified")
	}

	// Expiry re-check: a hit past its signed expiry is refused and dropped.
	if got, ok := cache.lookup(raw, now.Add(2*time.Hour), fp); ok {
		t.Fatalf("expired entry served: %q", got)
	}
	if _, ok := cache.lookup(raw, now, fp); ok {
		t.Fatal("expired entry not dropped")
	}

	// Secret roll: an entry verified under another generation is refused.
	cache.store(raw, token, expires, fp)
	if _, ok := cache.lookup(raw, now, other.SigningFingerprint()); ok {
		t.Fatal("foreign-secret entry served")
	}

	// Remove (logout) drops the entry.
	cache.store(raw, token, expires, fp)
	cache.remove(raw)
	if _, ok := cache.lookup(raw, now, fp); ok {
		t.Fatal("removed entry served")
	}

	// Bounded shards: filling past the cap never grows the map.
	target := cache.shard(raw)
	var filler []string
	for i := 0; len(filler) < authCacheCap+2; i++ {
		signed, err := secrets.SignCookie("session_token", fmt.Sprintf("filler-%d", i), now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if cache.shard(signed) != target {
			continue
		}
		filler = append(filler, signed)
	}
	for _, signed := range filler {
		e, err := secrets.VerifyCookieExpires("session_token", signed, now, &token)
		if err != nil {
			t.Fatal(err)
		}
		cache.store(signed, token, e, fp)
	}
	if len(target.m) > authCacheCap {
		t.Fatalf("shard grew to %d entries, cap %d", len(target.m), authCacheCap)
	}

	// Concurrent store/lookup on one shard is race-free (exercised under -race
	// in the acceptance run).
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := fmt.Sprintf("concurrent-%d", i)
			signed, err := secrets.SignCookie("session_token", value, now.Add(time.Hour))
			if err != nil {
				t.Error(err)
				return
			}
			var token string
			e, err := secrets.VerifyCookieExpires("session_token", signed, now, &token)
			if err != nil {
				t.Error(err)
				return
			}
			cache.store(signed, value, e, fp)
			if got, ok := cache.lookup(signed, now, fp); !ok || got != value {
				t.Errorf("concurrent lookup = %q, %v", got, ok)
			}
			cache.remove(signed)
		}(i)
	}
	wg.Wait()
}

// authTestServer builds a real-time server (authorization expiry tests need a
// moving clock) with a logged-in session, and returns the app, the user and
// the *unescaped* signed cookie value so tests can mutate it.
func authTestServer(t *testing.T) (*Server, *httptest.Server, database.User, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("authfast-e2e")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if !app.authFast || app.authCache == nil {
		t.Fatal("auth fast path not enabled by default")
	}
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
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)
	return app, server, user, signed
}

// authRequest sends one request with the given raw session_token cookie value
// ("" sends none) and returns the response.
func authRequest(t *testing.T, server *httptest.Server, method, path, raw string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "chat.test"
	request.Header.Set("Accept", "*/*")
	if raw != "" {
		request.Header.Set("Cookie", "session_token="+rails.EscapeCookie(raw))
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response
}

func sessionCookie(responses ...*http.Response) *http.Cookie {
	for _, r := range responses {
		for _, c := range r.Cookies() {
			if c.Name == "session_token" {
				return c
			}
		}
	}
	return nil
}

func sessionToken(t *testing.T, app *Server, user database.User) string {
	t.Helper()
	var token string
	if err := app.DB.Read.QueryRow("SELECT token FROM sessions WHERE user_id=?", user.ID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}

// TestAuthFastRejectsTamperingAndRevocation runs the poisoning battery against
// a live server: every invalid shape is rejected on every repetition, and a
// validated cookie keeps working (and keeps its bytes — no rewrite) across
// cache hits.
func TestAuthFastRejectsTamperingAndRevocation(t *testing.T) {
	app, server, user, raw := authTestServer(t)
	ctx := context.Background()

	response := authRequest(t, server, "GET", "/rooms/1", raw)
	if response.StatusCode != 200 {
		t.Fatalf("valid cookie: %d", response.StatusCode)
	}
	// The verified value entered the cache.
	if _, ok := app.authCache.lookup(raw, app.DB.Now(), app.Secrets.SigningFingerprint()); !ok {
		t.Fatal("valid cookie not cached after first request")
	}

	// Cache hits keep serving, and never rewrite the session_token cookie.
	for i := 0; i < 3; i++ {
		response := authRequest(t, server, "GET", "/rooms/1", raw)
		if response.StatusCode != 200 {
			t.Fatalf("repeat %d: %d", i, response.StatusCode)
		}
		if sessionCookie(response) != nil {
			t.Fatalf("repeat %d rewrote the session cookie", i)
		}
	}

	// Tampered byte: rejected every time, never cached.
	tamperedRaw := string(append([]byte(raw[:len(raw)/2]), append([]byte{raw[len(raw)/2] ^ 0x40}, raw[len(raw)/2+1:]...)...))
	for i := 0; i < 3; i++ {
		response := authRequest(t, server, "GET", "/rooms/1", tamperedRaw)
		if response.StatusCode != 302 || response.Header.Get("Location") != "http://chat.test/session/new" {
			t.Fatalf("tampered %d: %d %s", i, response.StatusCode, response.Header.Get("Location"))
		}
	}
	if _, ok := app.authCache.lookup(tamperedRaw, app.DB.Now(), app.Secrets.SigningFingerprint()); ok {
		t.Fatal("tampered value cached")
	}

	// Wrong secret: never verified, never cached.
	other, err := rails.NewSecrets("other-secret")
	if err != nil {
		t.Fatal(err)
	}
	wrongSecret, err := other.SignCookie("session_token", "some-token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if response := authRequest(t, server, "GET", "/rooms/1", wrongSecret); response.StatusCode != 302 {
		t.Fatalf("wrong secret: %d", response.StatusCode)
	}

	// Expired signature: rejected before any cache involvement.
	expired, err := app.Secrets.SignCookie("session_token", "some-token", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if response := authRequest(t, server, "GET", "/rooms/1", expired); response.StatusCode != 302 {
		t.Fatalf("expired signature: %d", response.StatusCode)
	}

	// Signed expiry re-checked from the cache: a cookie that verifies once and
	// then passes its expiry must be refused without re-running verification.
	known := sessionToken(t, app, user)
	short, err := app.Secrets.SignCookie("session_token", known, time.Now().Add(400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if response := authRequest(t, server, "GET", "/rooms/1", short); response.StatusCode != 200 {
		t.Fatalf("short-lived valid: %d", response.StatusCode)
	}
	if _, ok := app.authCache.lookup(short, app.DB.Now(), app.Secrets.SigningFingerprint()); !ok {
		t.Fatal("short-lived cookie not cached")
	}
	time.Sleep(600 * time.Millisecond)
	if response := authRequest(t, server, "GET", "/rooms/1", short); response.StatusCode != 302 {
		t.Fatalf("expired cached cookie served: %d", response.StatusCode)
	}
	if _, ok := app.authCache.lookup(short, app.DB.Now(), app.Secrets.SigningFingerprint()); ok {
		t.Fatal("expired cookie still cached")
	}

	// A cookie without a signed expiry verifies but is never cached (there is
	// nothing to re-check against).
	noExpiry, err := app.Secrets.SignCookie("session_token", known, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if response := authRequest(t, server, "GET", "/rooms/1", noExpiry); response.StatusCode != 200 {
		t.Fatalf("no-expiry cookie: %d", response.StatusCode)
	}
	if _, ok := app.authCache.lookup(noExpiry, app.DB.Now(), app.Secrets.SigningFingerprint()); ok {
		t.Fatal("expiry-less cookie cached")
	}

	// Revoked session: the per-request read re-checks and rejects, even though
	// the cookie value is still in the verification cache.
	if _, err := app.DB.Write.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.authCache.lookup(raw, app.DB.Now(), app.Secrets.SigningFingerprint()); !ok {
		t.Fatal("valid cookie dropped before revocation check")
	}
	if response := authRequest(t, server, "GET", "/rooms/1", raw); response.StatusCode != 302 {
		t.Fatalf("revoked session: %d", response.StatusCode)
	}

	// Banned user: same rejection through the joined read.
	token, err := app.DB.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	bannedCookie, err := app.Secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if response := authRequest(t, server, "GET", "/rooms/1", bannedCookie); response.StatusCode != 200 {
		t.Fatalf("fresh session: %d", response.StatusCode)
	}
	if err := app.DB.BanUser(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if response := authRequest(t, server, "GET", "/rooms/1", bannedCookie); response.StatusCode != 302 {
		t.Fatalf("banned user: %d", response.StatusCode)
	}

	// Logout removes the value from the verification cache.
	app2, server2, _, raw2 := authTestServer(t)
	if response := authRequest(t, server2, "GET", "/rooms/1", raw2); response.StatusCode != 200 {
		t.Fatalf("fresh: %d", response.StatusCode)
	}
	if response := authRequest(t, server2, "DELETE", "/session", raw2); response.StatusCode != 302 {
		t.Fatalf("logout: %d", response.StatusCode)
	}
	if _, ok := app2.authCache.lookup(raw2, app2.DB.Now(), app2.Secrets.SigningFingerprint()); ok {
		t.Fatal("logout did not remove the cached value")
	}
}

// TestAuthFastHourlyRefresh pins the write-only-when-changed behaviour: a
// fresh session is never re-signed, a session past its hour is refreshed
// exactly once (same cookie bytes, kept), and the next request stops writing.
func TestAuthFastHourlyRefresh(t *testing.T) {
	app, server, user, raw := authTestServer(t)
	ctx := context.Background()

	response := authRequest(t, server, "GET", "/rooms/1", raw)
	if response.StatusCode != 200 {
		t.Fatalf("fresh: %d", response.StatusCode)
	}
	if sessionCookie(response) != nil {
		t.Fatal("fresh session re-signed")
	}

	// Push the session past its hourly gate.
	if _, err := app.DB.Write.ExecContext(ctx, "UPDATE sessions SET last_active_at=? WHERE user_id=?", database.Stamp(app.DB.Now().Add(-2*time.Hour)), user.ID); err != nil {
		t.Fatal(err)
	}
	response = authRequest(t, server, "GET", "/rooms/1", raw)
	if response.StatusCode != 200 {
		t.Fatalf("stale session: %d", response.StatusCode)
	}
	refreshed := sessionCookie(response)
	if refreshed == nil {
		t.Fatal("stale session not refreshed")
	}
	var token string
	if err := app.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(refreshed.Value), app.DB.Now(), &token); err != nil {
		t.Fatalf("refreshed cookie invalid: %v", err)
	}
	if token != sessionToken(t, app, user) {
		t.Fatalf("refreshed cookie carries %q, want the session token", token)
	}

	// The refreshed session is fresh again: the next request writes nothing.
	response = authRequest(t, server, "GET", "/rooms/1", raw)
	if response.StatusCode != 200 {
		t.Fatalf("post-refresh: %d", response.StatusCode)
	}
	if sessionCookie(response) != nil {
		t.Fatal("refreshed session re-signed on the next request")
	}
}

// testAuthFastPair serves two servers over one database — CAMPFIRE_AUTH_FAST
// default on and off — with frozen time, so the same request can be compared
// byte for byte. Both servers keep the fastdb pool (the auth fast path is the
// only variable).
func testAuthFastPair(t *testing.T) (on, off *Server, onServer, offServer *httptest.Server, raw string, user database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("authfast-parity")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, err = db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("setup rooms: %v %d", err, len(rooms))
	}
	if _, err := db.CreateMessage(ctx, user.ID, rooms[0].ID, "", "<p>hello</p>", "hello"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CAMPFIRE_AUTH_FAST", "on")
	on, err = New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_AUTH_FAST", "off")
	off, err = New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	t.Setenv("CAMPFIRE_AUTH_FAST", "")
	if !on.authFast || on.authCache == nil {
		t.Fatal("fast server has no auth fast path")
	}
	if off.authFast || off.authCache != nil {
		t.Fatal("off server has an auth fast path")
	}
	onServer = httptest.NewServer(on)
	t.Cleanup(onServer.Close)
	offServer = httptest.NewServer(off)
	t.Cleanup(offServer.Close)
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return on, off, onServer, offServer, signed, user
}

// compareAuthParity runs one request against both servers and requires the
// observable response to match: status, body bytes, Location, Content-Type,
// ETag, a byte-identical session_token cookie when either side writes one, and
// (for the encrypted browser session cookie, whose ciphertext is random) the
// same attribute set and the same decrypted return_to value.
func compareAuthParity(t *testing.T, onServer, offServer *httptest.Server, on *Server, path, rawCookie string) {
	t.Helper()
	var cookie *http.Cookie
	if rawCookie != "" {
		cookie = &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(rawCookie)}
	}
	fast, fastBody := parityGet(t, onServer, path, cookie)
	legacy, legacyBody := parityGet(t, offServer, path, cookie)
	if fast.StatusCode != legacy.StatusCode {
		t.Fatalf("%s: fast %d != legacy %d", path, fast.StatusCode, legacy.StatusCode)
	}
	if string(fastBody) != string(legacyBody) {
		t.Fatalf("%s: fast body (%d bytes) != legacy body (%d bytes)", path, len(fastBody), len(legacyBody))
	}
	for _, header := range []string{"Location", "Content-Type", "ETag"} {
		if fast.Header.Get(header) != legacy.Header.Get(header) {
			t.Fatalf("%s: %s %q != %q", path, header, fast.Header.Get(header), legacy.Header.Get(header))
		}
	}
	fastSession, legacySession := sessionCookie(fast), sessionCookie(legacy)
	if (fastSession == nil) != (legacySession == nil) {
		t.Fatalf("%s: session_token write mismatch", path)
	}
	if fastSession != nil {
		if fastSession.Value != legacySession.Value {
			t.Fatalf("%s: refreshed cookie bytes differ", path)
		}
		if fastSession.MaxAge != legacySession.MaxAge || fastSession.Path != legacySession.Path || fastSession.HttpOnly != legacySession.HttpOnly || fastSession.Secure != legacySession.Secure {
			t.Fatalf("%s: refreshed cookie attributes differ", path)
		}
	}
	var fastBrowser, legacyBrowser *http.Cookie
	for _, c := range fast.Cookies() {
		if c.Name == browserSessionCookie {
			fastBrowser = c
		}
	}
	for _, c := range legacy.Cookies() {
		if c.Name == browserSessionCookie {
			legacyBrowser = c
		}
	}
	if (fastBrowser == nil) != (legacyBrowser == nil) {
		t.Fatalf("%s: browser session cookie write mismatch", path)
	}
	if fastBrowser != nil {
		if fastBrowser.Path != legacyBrowser.Path || fastBrowser.HttpOnly != legacyBrowser.HttpOnly || fastBrowser.Secure != legacyBrowser.Secure || fastBrowser.MaxAge != legacyBrowser.MaxAge {
			t.Fatalf("%s: browser session cookie attributes differ", path)
		}
		var fastState, legacyState map[string]any
		if err := on.Secrets.DecryptCookie(fastBrowser.Name, rails.UnescapeCookie(fastBrowser.Value), on.DB.Now(), &fastState); err != nil {
			t.Fatalf("decrypt fast browser cookie: %v", err)
		}
		if err := on.Secrets.DecryptCookie(legacyBrowser.Name, rails.UnescapeCookie(legacyBrowser.Value), on.DB.Now(), &legacyState); err != nil {
			t.Fatalf("decrypt legacy browser cookie: %v", err)
		}
		if fastState["return_to_after_authenticating"] != legacyState["return_to_after_authenticating"] {
			t.Fatalf("%s: return_to %v != %v", path, fastState["return_to_after_authenticating"], legacyState["return_to_after_authenticating"])
		}
	}
}

// TestAuthFastParity runs the auth scenarios through both servers — fast auth
// on and off over the same database — and requires byte-identical responses.
// CAMPFIRE_FROZEN_TIME makes every timestamp and signed cookie deterministic.
func TestAuthFastParity(t *testing.T) {
	on, _, onServer, offServer, raw, user := testAuthFastPair(t)
	ctx := context.Background()

	// Valid cookie: 200 parity, cache hit on the fast server, no writes.
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", raw)
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", raw)

	// No cookie: authenticated redirect parity.
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", "")

	// Tampered byte: rejection parity on every repetition.
	tamperedRaw := string(append([]byte(raw[:len(raw)/2]), append([]byte{raw[len(raw)/2] ^ 0x40}, raw[len(raw)/2+1:]...)...))
	for i := 0; i < 2; i++ {
		compareAuthParity(t, onServer, offServer, on, "/rooms/1", tamperedRaw)
	}

	// Wrong secret and expired signature: rejection parity.
	other, err := rails.NewSecrets("other-secret")
	if err != nil {
		t.Fatal(err)
	}
	wrongSecret, err := other.SignCookie("session_token", "some-token", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", wrongSecret)
	expired, err := on.Secrets.SignCookie("session_token", "some-token", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", expired)

	// requireUnauthenticated reads the same cached verification.
	compareAuthParity(t, onServer, offServer, on, "/session/new", raw)

	// Hourly refresh: each side sees the same stale state (the first request
	// consumes the staleness, so the update is re-applied before the second
	// side runs) and both write the same refreshed cookie.
	if _, err := on.DB.Write.ExecContext(ctx, "UPDATE sessions SET last_active_at=? WHERE user_id=?", database.Stamp(on.DB.Now().Add(-2*time.Hour)), user.ID); err != nil {
		t.Fatal(err)
	}
	fast, _ := parityGet(t, onServer, "/rooms/1", &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(raw)})
	if fast.StatusCode != 200 {
		t.Fatalf("fast refresh: %d", fast.StatusCode)
	}
	fastRefreshed := sessionCookie(fast)
	if fastRefreshed == nil {
		t.Fatal("fast side did not refresh the stale session")
	}
	if _, err := on.DB.Write.ExecContext(ctx, "UPDATE sessions SET last_active_at=? WHERE user_id=?", database.Stamp(on.DB.Now().Add(-2*time.Hour)), user.ID); err != nil {
		t.Fatal(err)
	}
	legacy, _ := parityGet(t, offServer, "/rooms/1", &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(raw)})
	if legacy.StatusCode != 200 {
		t.Fatalf("legacy refresh: %d", legacy.StatusCode)
	}
	legacyRefreshed := sessionCookie(legacy)
	if legacyRefreshed == nil {
		t.Fatal("legacy side did not refresh the stale session")
	}
	if fastRefreshed.Value != legacyRefreshed.Value {
		t.Fatalf("refreshed cookie bytes differ: %q != %q", fastRefreshed.Value, legacyRefreshed.Value)
	}
	// Both sides are quiet again on the next request.
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", raw)

	// Revoked session: both reject through the per-request read.
	if _, err := on.DB.Write.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", raw)

	// Banned user: both reject through the joined read.
	token, err := on.DB.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	banned, err := on.Secrets.SignCookie("session_token", token, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", banned)
	if err := on.DB.BanUser(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	compareAuthParity(t, onServer, offServer, on, "/rooms/1", banned)
}

// TestAuthFastOffBuildsNoCache pins the A/B switch shape:
// CAMPFIRE_AUTH_FAST=off constructs a server without the cache and the joined
// read (authFast off), which the parity test above holds byte-identical.
func TestAuthFastOffBuildsNoCache(t *testing.T) {
	t.Setenv("CAMPFIRE_AUTH_FAST", "off")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, err := rails.NewSecrets("off-build")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.authFast {
		t.Fatal("CAMPFIRE_AUTH_FAST=off left the fast path on")
	}
	if app.authCache != nil {
		t.Fatal("CAMPFIRE_AUTH_FAST=off built a cache")
	}
	// verifiedSessionToken falls back to the full verification: still
	// accepts a valid cookie and rejects a tampered one.
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
	if _, err := app.verifiedSessionToken(signed, app.DB.Now()); err != nil {
		t.Fatalf("valid cookie rejected without the fast path: %v", err)
	}
	if _, err := app.verifiedSessionToken("garbage", app.DB.Now()); err == nil {
		t.Fatal("garbage cookie accepted without the fast path")
	}
}
