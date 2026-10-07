package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func cachedRequest(t *testing.T, app *Server, cookie *http.Cookie, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://cache.test"+path, nil)
	request.AddCookie(cookie)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	writer := httptest.NewRecorder()
	front.Deflate(app).ServeHTTP(writer, request)
	return writer
}
func cacheHits(app *Server) uint64 {
	app.responses.mu.Lock()
	defer app.responses.mu.Unlock()
	return app.responses.hits
}
func cacheEntries(app *Server) int {
	app.responses.mu.Lock()
	defer app.responses.mu.Unlock()
	return len(app.responses.entries)
}
func foreignWriter(t *testing.T, app *Server) *sql.DB {
	t.Helper()
	var index int
	var name, path string
	if err := app.DB.Read.QueryRow("PRAGMA database_list").Scan(&index, &name, &path); err != nil {
		t.Fatal(err)
	}
	connection, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}
func execForeign(t *testing.T, connection *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := connection.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func TestResponseCacheFinishedBodiesHeadersAndVariants(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	_, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "cache-body", "<p>whole response cached literal csrf-token stays text</p>", "whole response cached literal csrf-token stays text")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{fmt.Sprintf("/rooms/%d", rooms[0].ID), fmt.Sprintf("/rooms/%d/messages", rooms[0].ID), "/users/me/sidebar", "/searches?q=whole"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			first := cachedRequest(t, app, cookie, "GET", path, nil)
			hits := cacheHits(app)
			second := cachedRequest(t, app, cookie, "GET", path, nil)
			if first.Code != 200 || second.Code != 200 || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) || cacheHits(app) != hits+1 {
				t.Fatal("no identical authenticated hit", first.Code, second.Code, cacheHits(app), hits)
			}
			if first.Header().Get("ETag") != second.Header().Get("ETag") || second.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("validators/security headers changed")
			}
			limit := app.responses.limit
			app.responses.limit = 0
			uncached := cachedRequest(t, app, cookie, "GET", path, nil)
			app.responses.limit = limit
			if !bytes.Equal(second.Body.Bytes(), uncached.Body.Bytes()) {
				t.Fatal("cached response differs from native rendering")
			}
			head := cachedRequest(t, app, cookie, "HEAD", path, nil)
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != first.Header().Get("ETag") {
				t.Fatal("HEAD representation")
			}
			conditional := cachedRequest(t, app, cookie, "GET", path, map[string]string{"If-None-Match": first.Header().Get("ETag")})
			if conditional.Code != 304 || conditional.Body.Len() != 0 {
				t.Fatal("conditional hit", conditional.Code)
			}
			zipped := cachedRequest(t, app, cookie, "GET", path, map[string]string{"Accept-Encoding": "gzip"})
			hits = cacheHits(app)
			again := cachedRequest(t, app, cookie, "GET", path, map[string]string{"Accept-Encoding": "gzip"})
			if zipped.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(zipped.Body.Bytes(), again.Body.Bytes()) || cacheHits(app) != hits+1 {
				t.Fatal("completed gzip not retained")
			}
			reader, err := gzip.NewReader(bytes.NewReader(again.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			plain, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || !bytes.Equal(plain, first.Body.Bytes()) {
				t.Fatal("incomplete or different gzip", err)
			}
		})
	}
	path := paths[0]
	for _, headers := range []map[string]string{{"Turbo-Frame": "user_sidebar"}, {"User-Agent": "Mozilla/5.0 (Macintosh) Chrome/130.0"}, {"X-Forwarded-Host": "other.test:8001"}, {"Origin": "https://other.test"}} {
		hits := cacheHits(app)
		response := cachedRequest(t, app, cookie, "GET", path, headers)
		if response.Code != 200 || cacheHits(app) != hits {
			t.Fatal("render variants aliased", headers)
		}
	}
	// Per-request cookies are never copied from the entry.
	hit := cachedRequest(t, app, cookie, "GET", path, nil)
	if !strings.Contains(hit.Header().Get("Set-Cookie"), "last_room=") {
		t.Fatal("hit lost fresh room cookie")
	}
}
func TestResponseCacheRechecksIdentityAndReachability(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	member, err := app.DB.CreateUser(ctx, "Cache member", "cache-member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Cache private", []int64{user.ID, member.ID})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d", room.ID)
	request := func(c *http.Cookie) *httptest.ResponseRecorder { return cachedRequest(t, app, c, "GET", path, nil) }
	request(cookie)
	memberToken, err := app.DB.StartSession(ctx, member.ID, "member", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	memberRaw, _ := app.Secrets.SignCookie("session_token", memberToken, time.Now().Add(time.Hour))
	memberCookie := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(memberRaw)}
	memberPage := request(memberCookie)
	if !strings.Contains(memberPage.Body.String(), fmt.Sprintf(`<meta name="current-user-id" content="%d">`, member.ID)) {
		t.Fatal("another user's cached layout leaked")
	}
	request(cookie)
	before := cacheHits(app)
	if request(cookie).Code != 200 || cacheHits(app) != before+1 {
		t.Fatal("warm private response missed")
	}
	token, err := app.DB.StartSession(ctx, user.ID, "other session", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := app.Secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	other := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	request(other)
	before = cacheHits(app)
	if request(other).Code != 200 || cacheHits(app) != before+1 {
		t.Fatal("second session did not cache")
	}
	var original string
	if err := app.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), app.DB.Now(), &original); err != nil {
		t.Fatal(err)
	}
	expiredRaw, _ := app.Secrets.SignCookie("session_token", token, time.Now().Add(-time.Second))
	if request(&http.Cookie{Name: "session_token", Value: rails.EscapeCookie(expiredRaw)}).Code != 302 {
		t.Fatal("expired cookie served cached page")
	}
	foreign := foreignWriter(t, app)
	execForeign(t, foreign, "DELETE FROM sessions WHERE token=?", original)
	before = cacheHits(app)
	if request(cookie).Code != 302 || cacheHits(app) != before {
		t.Fatal("revoked session served cached body")
	}
	request(other)
	execForeign(t, foreign, "DELETE FROM memberships WHERE room_id=? AND user_id=?", room.ID, user.ID)
	before = cacheHits(app)
	if request(other).Code != 302 || cacheHits(app) != before {
		t.Fatal("revoked membership served cached body")
	}
	execForeign(t, foreign, "UPDATE users SET status=2 WHERE id=?", user.ID)
	if request(other).Code != 302 {
		t.Fatal("disabled identity served cached body")
	}
}
func TestResponseCacheForeignChangesFlashAndForgery(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	original := cachedRequest(t, app, cookie, "GET", path, nil)
	foreign := foreignWriter(t, app)
	execForeign(t, foreign, "UPDATE rooms SET name='Foreign room name' WHERE id=?", rooms[0].ID)
	hits := cacheHits(app)
	fresh := cachedRequest(t, app, cookie, "GET", path, nil)
	if cacheHits(app) != hits || !strings.Contains(fresh.Body.String(), "Foreign room name") || bytes.Equal(original.Body.Bytes(), fresh.Body.Bytes()) {
		t.Fatal("foreign commit did not invalidate")
	}
	before := cacheEntries(app)
	raw, err := app.Secrets.EncryptCookie(browserSessionCookie, map[string]any{"flash": map[string]any{"flashes": map[string]any{"notice": "one-time cache notice"}, "discard": []any{}}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://cache.test"+path, nil)
	request.AddCookie(cookie)
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: rails.EscapeCookie(raw)})
	writer := httptest.NewRecorder()
	front.Deflate(app).ServeHTTP(writer, request)
	if !strings.Contains(writer.Body.String(), "one-time cache notice") || cacheEntries(app) != before {
		t.Fatal("flash cached or missing")
	}
	if strings.Contains(cachedRequest(t, app, cookie, "GET", path, nil).Body.String(), "one-time cache notice") {
		t.Fatal("one-time flash leaked")
	}
	request = httptest.NewRequest("POST", fmt.Sprintf("http://cache.test/rooms/%d/messages", rooms[0].ID), strings.NewReader("message[body]=forged"))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://attacker.test")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	writer = httptest.NewRecorder()
	app.ServeHTTP(writer, request)
	if writer.Code != 422 {
		t.Fatal("cached GET bypassed forgery protection", writer.Code)
	}
}
func TestResponseCacheDoesNotAdmitAConcurrentCommit(t *testing.T) {
	app, _, cookie, user := testApp(t)
	request := httptest.NewRequest("GET", "http://cache.test/rooms/1", nil)
	request.AddCookie(cookie)
	request = request.WithContext(context.WithValue(request.Context(), requestInfoKey{}, &requestInfo{host: request.Host, origin: app.origin(request)}))
	writer := httptest.NewRecorder()
	buffer := &responseBuffer{ResponseWriter: writer, server: app}
	session, request := app.withBrowserSession(buffer, request)
	app.beginResponseCache(request)
	requestMetadata(request.Context()).response.user = user.ID
	if app.responseHit(request) != nil {
		t.Fatal("unexpected initial hit")
	}
	session.Write([]byte("<!DOCTYPE html><p>render read before commit</p>"))
	foreign := foreignWriter(t, app)
	execForeign(t, foreign, "UPDATE accounts SET name='during render'")
	buffer.finish(request)
	if cacheEntries(app) != 0 {
		t.Fatal("pre-commit response admitted under a newer version")
	}
}
func TestResponseCacheBudgetAndHeadMiss(t *testing.T) {
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "")
	if n, err := responseCacheBudget(); err != nil || n != 64<<20 {
		t.Fatal(n, err)
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	if n, err := responseCacheBudget(); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "2048")
	if _, err := responseCacheBudget(); err == nil {
		t.Fatal("unbounded configuration accepted")
	}
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "1")
	app, _, cookie, _ := testApp(t)
	head := cachedRequest(t, app, cookie, "HEAD", "/users/me/sidebar", nil)
	if head.Code != 200 || head.Body.Len() != 0 || cacheEntries(app) != 0 {
		t.Fatal("HEAD populated cache")
	}
	cache := newResponseCache(4096)
	cache.get("start", 1)
	for i := 0; i < 100; i++ {
		key := fmt.Sprint(i)
		cache.put(&cachedResponse{key: key, version: 1, body: responsebody.NewPart(bytes.Repeat([]byte("x"), 256)), cost: 512})
	}
	if cache.size > cache.limit || len(cache.entries) != 8 {
		t.Fatal("byte budget exceeded", cache.size, len(cache.entries))
	}
	cache.get("new", 2)
	cache.put(&cachedResponse{key: "stale", version: 1, cost: 1})
	if cache.get("stale", 1) != nil || cache.version != 2 {
		t.Fatal("old request rolled back cache generation")
	}
}

func TestResponseCacheForeignFragmentEditsWithoutTimestamps(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "foreign-cache", "<p>Original foreign body</p>", "Original foreign body"); err != nil {
		t.Fatal(err)
	}
	var id, creator int64
	if err := app.DB.Read.QueryRow("SELECT id,creator_id FROM messages WHERE room_id=? ORDER BY id DESC LIMIT 1", rooms[0].ID).Scan(&id, &creator); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	cachedRequest(t, app, cookie, "GET", path, nil)
	cachedRequest(t, app, cookie, "GET", path, nil)
	messagePath := path + "/messages"
	warmMessages := cachedRequest(t, app, cookie, "GET", messagePath, nil)
	oldEtag := warmMessages.Header().Get("ETag")
	foreign := foreignWriter(t, app)
	execForeign(t, foreign, "UPDATE action_text_rich_texts SET body='<p>Foreign unchanged timestamp body</p>' WHERE record_type='Message' AND record_id=?", id)
	fresh := cachedRequest(t, app, cookie, "GET", path, nil)
	conditional := cachedRequest(t, app, cookie, "GET", messagePath, map[string]string{"If-None-Match": oldEtag})
	if conditional.Code != 200 || conditional.Header().Get("ETag") == oldEtag || !strings.Contains(conditional.Body.String(), "Foreign unchanged timestamp body") {
		t.Fatal("false304 for foreign body edit")
	}
	bodyEtag := conditional.Header().Get("ETag")
	execForeign(t, foreign, "UPDATE sessions SET last_active_at='2026-10-07 12:00:00.000000'")
	unchanged := cachedRequest(t, app, cookie, "GET", messagePath, map[string]string{"If-None-Match": bodyEtag})
	if unchanged.Code != 304 {
		t.Fatal("auth-only commit changed presentation validator", unchanged.Code)
	}
	if !strings.Contains(fresh.Body.String(), "Foreign unchanged timestamp body") {
		t.Fatal("stale message fragment")
	}
	execForeign(t, foreign, "UPDATE users SET name='Foreign fragment creator' WHERE id=?", creator)
	fresh = cachedRequest(t, app, cookie, "GET", path, nil)
	if !strings.Contains(fresh.Body.String(), "Foreign fragment creator") {
		t.Fatal("stale creator fragment")
	}
	stamp := app.DB.Now().UTC().Format("2006-01-02 15:04:05.000000")
	execForeign(t, foreign, "INSERT INTO boosts(booster_id,content,created_at,message_id,updated_at) VALUES(?,?,?,?,?)", user.ID, "🍊", stamp, id, stamp)
	fresh = cachedRequest(t, app, cookie, "GET", path, nil)
	if !strings.Contains(fresh.Body.String(), "🍊") {
		t.Fatal("stale new boost fragment")
	}
	execForeign(t, foreign, "UPDATE boosts SET content='🍋' WHERE message_id=?", id)
	fresh = cachedRequest(t, app, cookie, "GET", path, nil)
	if !strings.Contains(fresh.Body.String(), "🍋") || strings.Contains(fresh.Body.String(), "🍊") {
		t.Fatal("stale edited boost fragment")
	}
}

func TestNestedMessageCachePreservesRequestHostFiltering(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	body := `<action-text-attachment content-type="application/vnd.actiontext.opengraph-embed" href="https://same.example/story" filename="Story"></action-text-attachment>`
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "host-filter", body, "Story"); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{fmt.Sprintf("/rooms/%d", rooms[0].ID), fmt.Sprintf("/rooms/%d/messages", rooms[0].ID)} {
		for _, host := range []string{"same.example", "other.example", "same.example", "other.example"} {
			request := httptest.NewRequest("GET", "http://"+host+endpoint, nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			front.Deflate(app).ServeHTTP(response, request)
			if response.Code != 200 || strings.Contains(response.Body.String(), `href="https://same.example/story"`) != (host != "same.example") {
				t.Fatal("stale host-scoped message fragment", host, response.Code, response.Body.String())
			}
		}
	}
}
