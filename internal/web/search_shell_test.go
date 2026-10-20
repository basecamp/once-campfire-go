package web

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func TestSearchShellKeepsNavigationFresh(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("shell-navigation", "<p>shellneedle</p>")); err != nil {
		t.Fatal(err)
	}
	other, err := app.DB.CreateRoom(
		ctx,
		user.ID,
		"Rooms::Open",
		&sql.NullString{String: "Other", Valid: true},
		[]int64{user.ID},
	)
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
	if changed.StatusCode != 200 || changed.Header.Get("ETag") == etag ||
		!strings.Contains(body, "fresh-navigation") ||
		!strings.Contains(body, fmt.Sprintf(`href="/rooms/%d"`, other.ID)) {
		t.Fatal("retained layout hid fresh recent searches/return room")
	}
	if _, err := app.DB.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id=?", user.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	denied, body := get(changed.Header.Get("ETag"), other.ID)
	if denied.StatusCode != 200 ||
		strings.Contains(body, fmt.Sprintf(`href="/rooms/%d"`, other.ID)) {
		t.Fatal("return-room authorization was not observed afresh")
	}
}

func TestSearchShellPreservesValidator(t *testing.T) {
	app, _, _, user := testApp(t)
	p := page{User: user, Screen: "search", Title: "Search", BodyClass: "sidebar searches", Query: "coffee & <tea>", SearchResultCount: 2, RecentSearches: []string{"coffee", "<tea>"}, ReturnRoom: 12, MessagesHTML: "<div>one &amp; two</div>"}
	parts, err := app.Fragments.SearchParts(presentation.SearchInput{LayoutInput: layoutInput(p), Query: p.Query, SearchResultCount: p.SearchResultCount, RecentSearches: p.RecentSearches, ReturnRoom: p.ReturnRoom}, responsebody.NewPart([]byte(p.MessagesHTML)))
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	// The three-Part shape preserves writeRecorded's existing validator.
	const marker = "\x00test-marker\x00"
	p.MessagesHTML = template.HTML(marker)
	expected.Reset()
	if err := app.Presentation.ExecuteTemplate(&expected, "search", p); err != nil {
		t.Fatal(err)
	}
	before := httptest.NewRecorder()
	writeRecorded(before, 200, expected.String(), marker, parts[1])
	after := httptest.NewRecorder()
	writeParts(after, 200, parts)
	if before.Header().Get("ETag") != after.Header().Get("ETag") {
		t.Fatal("search validator changed")
	}
}
