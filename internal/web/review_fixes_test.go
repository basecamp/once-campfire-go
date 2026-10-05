package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

func TestResponseEncodingRespectsFrontSettings(t *testing.T) {
	t.Setenv("GZIP_COMPRESSION_ENABLED", "true")
	t.Setenv("GZIP_COMPRESSION_DISABLE_ON_AUTH", "false")
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	if got := responseEncoding(nil, request); got != "gzip" {
		t.Fatalf("default encoding: %q", got)
	}
	t.Setenv("GZIP_COMPRESSION_ENABLED", "false")
	if got := responseEncoding(nil, request); got != "" {
		t.Fatalf("disabled compression still negotiated: %q", got)
	}
	t.Setenv("GZIP_COMPRESSION_ENABLED", "true")
	t.Setenv("GZIP_COMPRESSION_DISABLE_ON_AUTH", "true")
	authed := request.Clone(request.Context())
	authed.Header.Set("Cookie", "session_token=x")
	if got := responseEncoding(nil, authed); got != "" {
		t.Fatalf("DisableGzipOnAuth ignored: %q", got)
	}
	rec := httptest.NewRecorder()
	rec.Header().Set("No-Gzip-Compression", "1")
	t.Setenv("GZIP_COMPRESSION_DISABLE_ON_AUTH", "false")
	if got := responseEncoding(rec, request); got != "" {
		t.Fatalf("No-Gzip-Compression ignored: %q", got)
	}
}

func TestRoomShellKeyIncludesPlatform(t *testing.T) {
	base := page{
		User:     database.User{ID: 1, Name: "Owner"},
		Room:     database.Room{ID: 1, Name: "Chat", Type: "Rooms::Open"},
		Platform: useragent.Platform{Chrome: true, Desktop: true, Browser: "Chrome", OperatingSystem: "macOS"},
	}
	other := base
	other.Platform = useragent.Platform{Safari: true, IOS: true, Mobile: true, Browser: "Safari", OperatingSystem: "iPhone"}
	if roomShellKey(base) == roomShellKey(other) {
		t.Fatal("platform change reused room shell key")
	}
}

func TestSearchCacheSkipsFlashAndTracksReturnRoom(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatal(rooms, err)
	}
	roomID := rooms[0].ID
	if _, err = app.DB.CreateMessage(ctx, user.ID, roomID, "s1", "<p>needle</p>", "needle"); err != nil {
		t.Fatal(err)
	}
	getSearch := func(returnRoom int64, notice string) string {
		t.Helper()
		req, err := http.NewRequest("GET", server.URL+"/searches?q=needle", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "*/*")
		req.AddCookie(cookie)
		req.AddCookie(&http.Cookie{Name: "last_room", Value: strconv.FormatInt(returnRoom, 10)})
		if notice != "" {
			expiry := app.DB.Now().AddDate(20, 0, 0)
			raw, err := app.Secrets.EncryptCookie(browserSessionCookie, map[string]any{
				"session_id": "test",
				"flash":      map[string]any{"discard": []any{}, "flashes": map[string]any{"notice": notice}},
			}, expiry)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: rails.EscapeCookie(raw)})
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("search status %d: %s", res.StatusCode, body)
		}
		return string(body)
	}

	first := getSearch(roomID, "")
	if !strings.Contains(first, "needle") {
		t.Fatal("search miss missing results")
	}
	hits := app.stats.searchHit.Load()
	second := getSearch(roomID, "")
	if app.stats.searchHit.Load() != hits+1 {
		t.Fatalf("expected search cache hit, hits=%d->%d", hits, app.stats.searchHit.Load())
	}
	if !strings.Contains(second, fmt.Sprintf("/rooms/%d", roomID)) {
		t.Fatal("cached search missing return room link")
	}

	other, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Other", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Warm again after CreateRoom bumps content generation so the next miss is from ReturnRoom.
	_ = getSearch(roomID, "")
	_ = getSearch(roomID, "")
	before := app.stats.searchMiss.Load()
	moved := getSearch(other.ID, "")
	if app.stats.searchMiss.Load() == before {
		t.Fatal("return room change should miss the search cache")
	}
	if !strings.Contains(moved, fmt.Sprintf("/rooms/%d", other.ID)) {
		t.Fatal("search kept the old return room")
	}

	flashed := getSearch(roomID, "Saved settings")
	if !strings.Contains(flashed, "Saved settings") {
		t.Fatal("flash missing from search render")
	}
	again := getSearch(roomID, "")
	if strings.Contains(again, "Saved settings") {
		t.Fatal("flash leaked into a later search cache hit")
	}
}

func TestSidebarInvalidatesAfterUserCreate(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	ctx := context.Background()
	get := func() {
		t.Helper()
		req, err := http.NewRequest("GET", server.URL+"/users/me/sidebar", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.Status)
		}
	}
	get()
	get()
	if app.stats.sidebarHit.Load() == 0 {
		t.Fatal("sidebar was not cached")
	}
	beforeMiss := app.stats.sidebarMiss.Load()
	if _, err := app.DB.CreateUser(ctx, "New Person", "new@test", "digest", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	get()
	if app.stats.sidebarMiss.Load() == beforeMiss {
		t.Fatal("CreateUser should invalidate sidebar cache via content generation")
	}
}

func TestCachedRoomSkipsGzipWhenDisabled(t *testing.T) {
	t.Setenv("GZIP_COMPRESSION_ENABLED", "true")
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err = app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "", "<p>pad pad pad pad</p>", "pad"); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	warm := func() *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		req.AddCookie(cookie)
		req.Header.Set("Accept-Encoding", "gzip")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := warm()
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	res = warm()
	if res.Header.Get("Content-Encoding") != "gzip" {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("warmup should compress: %v %s", res.Header, body[:min(200, len(body))])
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	t.Setenv("GZIP_COMPRESSION_ENABLED", "false")
	res = warm()
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.Header.Get("Content-Encoding") != "" {
		t.Fatalf("expected identity when gzip disabled, got %v", res.Header)
	}
	if !bytes.Contains(body, []byte("pad")) {
		t.Fatal("room body missing")
	}
}
