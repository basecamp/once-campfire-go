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

// TestCreateMessageStatementCount pins the statement budget of one
// POST /messages request on the database/sql write lane (ENGINE-45): queue
// on, fast reads off so every read is a counted database/sql statement,
// direct writes off so the create transaction's seven statements are
// counted too, checkpoints off the clock, one plain post. The number is
// part of the contract — the fresh-message view, the reused room record and
// the in-line lane are what keep it at 12 instead of 17 — so a change must
// update the count consciously. The direct lane's per-post stream is pinned
// separately, in TestCreateMessageFastWriteStatementCount.
func TestCreateMessageStatementCount(t *testing.T) {
	t.Setenv("CAMPFIRE_WRITE_QUEUE", "on")
	t.Setenv("CAMPFIRE_FASTDB", "off")
	t.Setenv("CAMPFIRE_FASTDB_WRITE", "off")
	t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, count, reset, dump, err := database.OpenCounting(dbPath, 4)
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
	cookie := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)

	form := url.Values{"message[body]": {"hello lane"}, "message[client_message_id]": {"count-1"}}.Encode()
	reset()
	response, data := parityPost(t, server, form, cookie)
	// The createMessage response for a browser-style Accept (", */*" tails)
	// is 406 by negotiation — the reference behaviour the parity test also
	// compares on; the message is created regardless.
	if response.StatusCode != 406 {
		t.Fatalf("post status %d body=%s, want 406-by-negotiation", response.StatusCode, data)
	}
	// 13 = 12 request statements + the PRAGMA data_version generation
	// observation on the pinned version connection (one read per request,
	// adopted from upstream ef00d84 to namespace the caches by observed
	// commits).
	if got := count(); got != 13 {
		t.Fatalf("POST /messages statements = %d, want 13 (12 + generation PRAGMA)\n%s", got, dump())
	}
}

// TestCreateMessageFastWriteStatementCount pins the statement budget of one
// POST /messages with the direct write lane on (the ENGINE-55 default): the
// create transaction's seven statements run as prepared statements on the
// fastdb connection, invisible to the counting driver, so the same request
// counts exactly 12−7 = 5 (the view statements; the write-side statements
// are pinned by text and order in the database package's
// TestFastWriteStatementStream, which records the direct lane's stream). The
// message still persists with its search row, so the count cut is not a
// dropped statement.
func TestCreateMessageFastWriteStatementCount(t *testing.T) {
	t.Setenv("CAMPFIRE_WRITE_QUEUE", "on")
	t.Setenv("CAMPFIRE_FASTDB", "off")
	t.Setenv("CAMPFIRE_FASTDB_WRITE", "on")
	t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, count, reset, _, err := database.OpenCounting(dbPath, 4)
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
	cookie := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)

	form := url.Values{"message[body]": {"hello lane"}, "message[client_message_id]": {"fast-count-1"}}.Encode()
	reset()
	response, data := parityPost(t, server, form, cookie)
	if response.StatusCode != 406 {
		t.Fatalf("post status %d body=%s, want 406-by-negotiation", response.StatusCode, data)
	}
	// 6 = 5 request statements + the PRAGMA data_version generation
	// observation (see TestCreateMessageStatementCount).
	if got := count(); got != 6 {
		t.Fatalf("direct-lane POST /messages statements = %d, want 6 (13 − 7 write-side)", got)
	}
	hits, err := db.Search(ctx, user.ID, "lane")
	if err != nil || len(hits) != 1 {
		t.Fatalf("search after direct-lane post: %v %v", hits, err)
	}
}

// TestWriteLaneParity compares the createMessage response across every
// combination of the two A/B switches using identically seeded databases:
// queue on/off (CAMPFIRE_WRITE_QUEUE) and fast reads on/off
// (CAMPFIRE_FASTDB, which now supplies messageViews' per-view reads). All
// responses must be byte-identical. The queued server posts through the
// batch path (its lane is held busy by a blocker job while the post is
// submitted), so the adaptive in-line path and the queued path are both
// compared byte-for-byte against the direct path.
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

	dbQU, pathQU, _ := seedLaneDB(t, "on")
	rootQU := filepath.Dir(pathQU)
	_, quServer, quCookie := laneServer(t, dbQU, "off", pathQU, rootQU)
	release := dbQU.LaneBlocker() // the post below can only batch behind it
	defer release()

	form := url.Values{"message[body]": {"hello lane"}, "message[client_message_id]": {"parity-1"}}.Encode()
	type result struct {
		status int
		body   []byte
	}
	run := func(server *httptest.Server, cookie *http.Cookie) result {
		response, data := parityPost(t, server, form, cookie)
		return result{response.StatusCode, data}
	}
	fast := run(qfServer, qfCookie)   // queue on,  fastdb on, in-line
	slow := run(qlServer, qlCookie)   // queue on,  fastdb off, in-line
	direct := run(dfServer, dfCookie) // queue off, fastdb on
	// The queued post runs on its own goroutine: it must be submitted while
	// the lane is still held (the blocker keeps the lane busy until
	// release(), so the post can only batch behind it), then the gate opens
	// and the batch commits.
	queuedResult := make(chan result, 1)
	go func() { queuedResult <- run(quServer, quCookie) }()
	time.Sleep(50 * time.Millisecond) // let the request reach the queue
	release()
	queued := <-queuedResult
	for name, got := range map[string]result{"fastdb=off": slow, "queue=off": direct, "queued": queued} {
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
