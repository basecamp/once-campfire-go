package web

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestExternalConnectionRefreshesContentAndPermissions(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) != 1 {
		t.Fatal(rooms, err)
	}
	open := rooms[0]
	if _, err = app.DB.CreateMessage(ctx, user.ID, open.ID, "seed", "<p>seed</p>", "seed"); err != nil {
		t.Fatal(err)
	}
	secret, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Secret Plans", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}

	body := getPath(t, server, cookie, "/rooms/"+strconv.FormatInt(open.ID, 10))
	if !strings.Contains(body, "seed") {
		t.Fatal("room page missing seeded message")
	}
	getPath(t, server, cookie, "/rooms/"+strconv.FormatInt(open.ID, 10))
	hits, _ := app.DB.PageCacheStats()
	if hits == 0 {
		t.Fatal("room message window was not cached")
	}
	sidebar := getPath(t, server, cookie, "/users/me/sidebar")
	getPath(t, server, cookie, "/users/me/sidebar")
	if app.stats.sidebarHit.Load() == 0 {
		t.Fatal("sidebar was not cached")
	}
	if !strings.Contains(sidebar, "Secret Plans") {
		t.Fatal("sidebar missing the closed room")
	}

	other, err := sql.Open("sqlite3", app.DB.FilePath()+"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stamp := database.Stamp(app.DB.Now())
	result, err := other.Exec(`INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES (?,?,?,?,?)`, "external-1", user.ID, open.ID, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(`INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)`, messageID, "<p>external-ping</p>", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(`DELETE FROM memberships WHERE user_id=? AND room_id=?`, user.ID, secret.ID); err != nil {
		t.Fatal(err)
	}

	next := getPath(t, server, cookie, "/rooms/"+strconv.FormatInt(open.ID, 10))
	if !strings.Contains(next, "external-ping") {
		t.Fatal("room page kept the cached window after an external insert")
	}
	sidebar = getPath(t, server, cookie, "/users/me/sidebar")
	if strings.Contains(sidebar, "Secret Plans") {
		t.Fatal("sidebar kept a room after an external membership delete")
	}
	revoked := getStatus(t, server, cookie, "/rooms/"+strconv.FormatInt(secret.ID, 10))
	if revoked != http.StatusFound {
		t.Fatalf("revoked room status %d", revoked)
	}
}

func TestAuthorRenameAndForeignBodyMissFragmentCache(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) != 1 {
		t.Fatal(rooms, err)
	}
	room := rooms[0]
	if _, err = app.DB.CreateMessage(ctx, user.ID, room.ID, "author-seed", "<p>seed</p>", "seed"); err != nil {
		t.Fatal(err)
	}
	path := "/rooms/" + strconv.FormatInt(room.ID, 10)
	first := getPath(t, server, cookie, path)
	if !strings.Contains(first, `data-reply-target="author">Owner</strong>`) || !strings.Contains(first, "seed") {
		t.Fatal("room page missing the author or the seeded message")
	}
	hits := app.stats.listHit.Load()
	second := getPath(t, server, cookie, path)
	if app.stats.listHit.Load() == hits {
		t.Fatal("message list was not cached")
	}
	if !strings.Contains(second, `data-reply-target="author">Owner</strong>`) {
		t.Fatal("warm message list dropped the author")
	}
	stamp := messageStamp(t, app, "author-seed")
	if err = app.DB.UpdateUser(ctx, user.ID, map[string]string{"name": "Renamed Owner"}, nil); err != nil {
		t.Fatal(err)
	}
	if messageStamp(t, app, "author-seed") != stamp {
		t.Fatal("renaming the author touched the message")
	}
	renamed := getPath(t, server, cookie, path)
	if !strings.Contains(renamed, `data-reply-target="author">Renamed Owner</strong>`) || strings.Contains(renamed, `data-reply-target="author">Owner</strong>`) {
		t.Fatal("cached message kept the old author")
	}

	other, err := sql.Open("sqlite3", app.DB.FilePath()+"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec(`UPDATE users SET name=?, updated_at=? WHERE id=?`, "Foreign Author", database.Stamp(app.DB.Now()), user.ID); err != nil {
		t.Fatal(err)
	}
	if messageStamp(t, app, "author-seed") != stamp {
		t.Fatal("foreign rename touched the message")
	}
	foreignName := getPath(t, server, cookie, path)
	if !strings.Contains(foreignName, `data-reply-target="author">Foreign Author</strong>`) {
		t.Fatal("foreign author rename stayed in the message fragment")
	}
	if _, err = other.Exec(`UPDATE action_text_rich_texts SET body=? WHERE record_type='Message' AND name='body' AND record_id=(SELECT id FROM messages WHERE client_message_id=?)`, "<p>foreign-body</p>", "author-seed"); err != nil {
		t.Fatal(err)
	}
	if messageStamp(t, app, "author-seed") != stamp {
		t.Fatal("foreign body edit touched the message")
	}
	foreignBody := getPath(t, server, cookie, path)
	if !strings.Contains(foreignBody, "foreign-body") || strings.Contains(foreignBody, ">seed<") {
		t.Fatal("foreign body edit stayed in the message fragment")
	}
}

func messageStamp(t *testing.T, app *Server, client string) string {
	t.Helper()
	var stamp string
	if err := app.DB.Read.QueryRow("SELECT updated_at FROM messages WHERE client_message_id=?", client).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	return stamp
}

func getPath(t *testing.T, server *httptest.Server, cookie *http.Cookie, path string) string {
	t.Helper()
	status, body := getRaw(t, server, cookie, path)
	if status != 200 {
		t.Fatalf("%s status %d: %s", path, status, body)
	}
	return body
}

func getStatus(t *testing.T, server *httptest.Server, cookie *http.Cookie, path string) int {
	t.Helper()
	status, _ := getRaw(t, server, cookie, path)
	return status
}

func getRaw(t *testing.T, server *httptest.Server, cookie *http.Cookie, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "*/*")
	req.AddCookie(cookie)
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}
