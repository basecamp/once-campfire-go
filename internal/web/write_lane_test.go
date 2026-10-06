package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// seedLaneDB opens a database for the write-lane parity harness: frozen time
// makes every timestamp deterministic, and the write-queue mode is fixed by
// CAMPFIRE_WRITE_QUEUE at open. The seed is identical across instances (the
// Setup user, a second user, the open room, two seed messages), so message
// ids line up between databases.
func seedLaneDB(t *testing.T, queueMode string) (*database.DB, string, *http.Cookie) {
	t.Helper()
	t.Setenv("CAMPFIRE_WRITE_QUEUE", queueMode)
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	user, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(ctx, "Alice", "alice@test", "digest", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("rooms: %v %v", rooms, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("seed-%d", i), fmt.Sprintf("<p>seed %d</p>", i), fmt.Sprintf("seed %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	secrets, err := rails.NewSecrets("write-lane-parity")
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return db, dbPath, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
}

// laneServer builds one server over the seeded database with the given fast
// read mode (the serve path of messageViews is what the parity compares).
func laneServer(t *testing.T, db *database.DB, fastdbMode, dbPath, root string) (*Server, *httptest.Server, *http.Cookie) {
	t.Helper()
	t.Setenv("CAMPFIRE_FASTDB", fastdbMode)
	secrets, err := rails.NewSecrets("write-lane-parity")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)
	token, err := db.StartSession(context.Background(), 1, "lane", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return app, server, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
}

// parityPost runs one message create against a server with a pinned Host so
// every permalink and stream target renders the same origin, and returns the
// response bytes.
func parityPost(t *testing.T, server *httptest.Server, body string, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest("POST", server.URL+"/rooms/1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "chat.test"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "text/vnd.turbo-stream.html, */*")
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

// TestWriteLaneParity compares the createMessage response across every
// combination of the two A/B switches using three identically seeded
// databases: queue on/off (CAMPFIRE_WRITE_QUEUE) and fast reads on/off
// (CAMPFIRE_FASTDB, which now supplies messageViews' per-view reads). All
// three responses must be byte-identical.
func TestWriteLaneParity(t *testing.T) {
	dbQF, pathQF, _ := seedLaneDB(t, "on")
	rootQF := filepath.Dir(pathQF)
	_, qfServer, qfCookie := laneServer(t, dbQF, "on", pathQF, rootQF)

	dbQL, pathQL, _ := seedLaneDB(t, "on")
	rootQL := filepath.Dir(pathQL)
	_, qlServer, qlCookie := laneServer(t, dbQL, "off", pathQL, rootQL)

	dbDF, pathDF, _ := seedLaneDB(t, "off")
	rootDF := filepath.Dir(pathDF)
	_, dfServer, dfCookie := laneServer(t, dbDF, "on", pathDF, rootDF)

	form := url.Values{"message[body]": {"hello lane"}, "message[client_message_id]": {"parity-1"}}.Encode()
	type result struct {
		status int
		body   []byte
	}
	run := func(server *httptest.Server, cookie *http.Cookie) result {
		response, data := parityPost(t, server, form, cookie)
		return result{response.StatusCode, data}
	}
	fast := run(qfServer, qfCookie)   // queue on,  fastdb on
	slow := run(qlServer, qlCookie)   // queue on,  fastdb off
	direct := run(dfServer, dfCookie) // queue off, fastdb on
	for name, got := range map[string]result{"fastdb=off": slow, "queue=off": direct} {
		if got.status != fast.status {
			t.Errorf("%s: status %d != %d", name, got.status, fast.status)
		}
		if string(got.body) != string(fast.body) {
			t.Errorf("%s: %d bytes != %d bytes\n%s\nvs\n%s", name, len(got.body), len(fast.body), got.body, fast.body)
		}
	}
	if len(fast.body) == 0 {
		t.Fatal("empty createMessage response")
	}
	// The message persisted on every database, newest first, with its search
	// row, and the unread bump touched the open room's other memberships.
	ctx := context.Background()
	for name, db := range map[string]*database.DB{"qf": dbQF, "ql": dbQL, "df": dbDF} {
		messages, err := db.Messages(ctx, 1, 0)
		if err != nil || len(messages) != 3 {
			t.Fatalf("%s: messages %d %v, want 3", name, len(messages), err)
		}
		hits, err := db.Search(ctx, 1, "lane")
		if err != nil || len(hits) != 1 {
			t.Errorf("%s: search %v %v, want one hit", name, hits, err)
		}
		var unread sql.NullString
		if err := db.Read.QueryRowContext(ctx, "SELECT unread_at FROM memberships WHERE room_id=1 AND user_id=2").Scan(&unread); err != nil {
			t.Fatal(err)
		}
		if !unread.Valid {
			t.Errorf("%s: observer unread_at not bumped", name)
		}
	}
}
