package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func TestSearchShellKeepsNavigationFresh(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "shell-navigation", "<p>shellneedle</p>", "shellneedle"); err != nil {
		t.Fatal(err)
	}
	other, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Open", "Other", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	get := func(etag string, room int64) (*http.Response, string) {
		t.Helper()
		r, _ := http.NewRequest("GET", server.URL+"/searches?q=shellneedle", nil)
		r.AddCookie(cookie)
		r.AddCookie(&http.Cookie{Name: "last_room", Value: fmt.Sprint(room)})
		r.Header.Set("If-None-Match", etag)
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, string(body)
	}
	first, _ := get("", rooms[0].ID)
	etag := first.Header.Get("ETag")
	if first.StatusCode != 200 || etag == "" {
		t.Fatal("initial search failed")
	}
	if same, body := get(etag, rooms[0].ID); same.StatusCode != 304 || body != "" {
		t.Fatal("unchanged search lost conditional response")
	}
	if err := app.DB.RecordSearch(ctx, user.ID, "fresh-navigation"); err != nil {
		t.Fatal(err)
	}
	changed, body := get(etag, other.ID)
	if changed.StatusCode != 200 || changed.Header.Get("ETag") == etag || !strings.Contains(body, "fresh-navigation") || !strings.Contains(body, fmt.Sprintf(`href="/rooms/%d"`, other.ID)) {
		t.Fatal("retained layout hid fresh recent searches/return room")
	}
	if _, err := app.DB.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id=?", user.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	denied, body := get(changed.Header.Get("ETag"), other.ID)
	if denied.StatusCode != 200 || strings.Contains(body, fmt.Sprintf(`href="/rooms/%d"`, other.ID)) {
		t.Fatal("return-room authorization was not observed afresh")
	}
}

func TestSearchShellPreservesBytesIdentityAndOwnership(t *testing.T) {
	app, _, _, user := testApp(t)
	base := page{User: user, Screen: "search", Title: "Search", BodyClass: "sidebar searches", Query: "coffee & <tea>", SearchResultCount: 2, RecentSearches: []string{"coffee", "<tea>"}, ReturnRoom: 12, MessagesHTML: "<div>one &amp; two</div>"}
	check := func(t *testing.T, p page) []responsebody.Part {
		t.Helper()
		var expected, actual bytes.Buffer
		if err := app.templates.ExecuteTemplate(&expected, "search", p); err != nil {
			t.Fatal(err)
		}
		parts, err := app.searchParts(p, responsebody.NewPart([]byte(p.MessagesHTML)))
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			if _, err := part.WriteTo(&actual); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(actual.Bytes(), expected.Bytes()) {
			t.Fatal("search shell differs from full template")
		}
		// The three-Part shape preserves writeRecorded's existing validator.
		const marker = "\x00test-marker\x00"
		p.MessagesHTML = template.HTML(marker)
		expected.Reset()
		if err := app.templates.ExecuteTemplate(&expected, "search", p); err != nil {
			t.Fatal(err)
		}
		before := httptest.NewRecorder()
		writeRecorded(before, 200, expected.String(), marker, parts[1])
		after := httptest.NewRecorder()
		writeParts(after, 200, parts)
		if before.Header().Get("ETag") != after.Header().Get("ETag") {
			t.Fatal("search validator changed")
		}
		return parts
	}
	first := check(t, base)
	check(t, base)
	t.Run("non-rendered user state reuses shell", func(t *testing.T) {
		entries, size := len(app.fragments.entries), app.fragments.bytes
		p := base
		p.User.Email = "changed@example.test"
		p.User.Password = "changed password digest"
		p.User.BotToken = "changed bot token"
		p.User.Status = 2
		check(t, p)
		if len(app.fragments.entries) != entries || app.fragments.bytes != size {
			t.Fatal("non-rendered user state retained another copy of unchanged shell HTML")
		}
	})
	changes := map[string]func(*page){
		"query":            func(p *page) { p.Query = "other <query>" },
		"count and body":   func(p *page) { p.SearchResultCount = 1; p.MessagesHTML = "<p>new result</p>" },
		"recents":          func(p *page) { p.RecentSearches = []string{"new", "<old>"} },
		"return room":      func(p *page) { p.ReturnRoom++ },
		"user":             func(p *page) { p.User.Name = "Other & user"; p.User.ID++; p.User.Role = 0 },
		"account":          func(p *page) { p.Account.HasLogo = true; p.Account.UpdatedAt = time.Unix(1700000000, 0) },
		"flash":            func(p *page) { p.Notice = "Saved & seen" },
		"error":            func(p *page) { p.Error = "Failed <again>" },
		"styles":           func(p *page) { p.CustomStyles = "<style>body{color:red}</style>" },
		"frame":            func(p *page) { p.Frame = true },
		"reload and vapid": func(p *page) { p.Reload = true; p.VAPIDPublicKey = "new-public-key" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { p := base; change(&p); check(t, p); check(t, base) })
	}
	var want bytes.Buffer
	for _, part := range first {
		part.WriteTo(&want)
	}
	// Eviction releases only cache references, not already selected response Parts.
	app.fragments.limit = 4096
	for i := 0; i < 50; i++ {
		app.fragments.putEntry(fragmentEntry{key: fmt.Sprint(i), part: responsebody.NewPart(make([]byte, 512))})
	}
	var retained bytes.Buffer
	for _, part := range first {
		part.WriteTo(&retained)
	}
	if !bytes.Equal(retained.Bytes(), want.Bytes()) {
		t.Fatal("eviction changed captured shell bytes")
	}
	for _, limit := range []int{0, 1, 32 << 20} {
		app.fragments = newFragmentCache(limit)
		check(t, base)
		check(t, base)
		if limit <= 1 && app.fragments.bytes != 0 {
			t.Fatal("disabled/oversized shell was retained")
		}
		if limit > 1 {
			for _, e := range app.fragments.entries {
				entry := e.Value.(fragmentEntry)
				if entry.shell == nil || entry.bytes != len(entry.key)+240+entry.shell.bytes+32+56*len(entry.shell.parts) {
					t.Fatal("shell payload must be charged once")
				}
			}
		}
	}
}
