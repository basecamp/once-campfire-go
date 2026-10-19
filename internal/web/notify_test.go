// Lever 5 pins for the posting request's notification step: with no unread
// subscriber the unread bump issues no statement and allocates nothing, an
// empty (already computed) mention list skips the webhook candidate lookup
// entirely, and each post-commit stage keeps its own budget — a slow or failed
// earlier stage cannot consume a later eligible stage's opportunity to run.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// notifyFixture builds a server over a counted database with the write lane
// on and fast reads off, so every read the notification step issues is a
// counted database/sql statement, plus one message in the owner's room.
func notifyFixture(t *testing.T) (*Server, *database.DB, database.Message, database.Room, func() int64, func(), func() []string) {
	t.Helper()
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
	message, err := db.CreateMessage(ctx, user.ID, rooms[0].ID, "hello", "<p>hello</p>", "hello")
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets("notify-pins")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if app.Push.VAPID != nil {
		t.Fatal("test expects Web Push unconfigured (no VAPID env)")
	}
	return app, db, message, rooms[0], count, reset, dump
}

// TestMessageCreatedZeroSubscribersStatements pins the zero-subscriber post's
// notification step at zero statements: the unread bump reads no membership
// row, and an empty mention list runs no webhook candidate query. The posting
// request's own budget drops by exactly the one member read this replaces
// (TestCreateMessageStatementCount).
func TestMessageCreatedZeroSubscribersStatements(t *testing.T) {
	app, _, message, room, count, reset, dump := notifyFixture(t)
	reset() // drop the fixture's seeding statements
	app.messageCreated(message, room, nil)
	app.enqueueWebhooks(message, room, []int64{})
	if got := count(); got != 0 {
		t.Fatalf("zero-subscriber notification statements = %d, want 0\n%s", got, strings.Join(dump(), "\n"))
	}
}

// TestMessageCreatedZeroSubscribersAllocs pins the same shortcut's allocation
// budget: the bump allocates no payload map, no stream name, no frame and no
// timer when no client on the server holds an unread subscription.
func TestMessageCreatedZeroSubscribersAllocs(t *testing.T) {
	app, _, message, room, _, _, _ := notifyFixture(t)
	allocs := testing.AllocsPerRun(1000, func() {
		app.messageCreated(message, room, nil)
	})
	if allocs != 0 {
		t.Fatalf("zero-subscriber messageCreated = %.2f allocs/op, want 0", allocs)
	}
	webhookAllocs := testing.AllocsPerRun(1000, func() {
		app.enqueueWebhooks(message, room, []int64{})
	})
	if webhookAllocs != 0 {
		t.Fatalf("empty-mention enqueueWebhooks = %.2f allocs/op, want 0", webhookAllocs)
	}
}

// TestEnqueueWebhooksEmptyMentionsSkipsCandidates pins the mention-list
// shortcut against the deriving caller: a nil list still derives and queries
// (the bot path), while the create path's already-computed empty list does no
// work at all.
func TestEnqueueWebhooksEmptyMentionsSkipsCandidates(t *testing.T) {
	app, _, message, room, count, reset, _ := notifyFixture(t)
	if room.Type == "Rooms::Direct" {
		t.Skip("the empty-mention shortcut applies to non-direct rooms")
	}
	reset()
	app.enqueueWebhooks(message, room, []int64{})
	if got := count(); got != 0 {
		t.Fatalf("empty-mention enqueueWebhooks statements = %d, want 0", got)
	}
	// A nil list (the bot and webhook callers) still derives the mentions from
	// the body; with none present that is still zero candidate queries.
	reset()
	app.enqueueWebhooks(message, room, nil)
	if got := count(); got != 0 {
		t.Fatalf("nil-mention enqueueWebhooks statements = %d, want 0", got)
	}
}

// cableUnreadSubscriber dials the app's real /cable endpoint and subscribes to
// UnreadRoomsChannel, so a test can observe the unread bump on the wire.
func cableUnreadSubscriber(t *testing.T, server *httptest.Server, cookie *http.Cookie) (*websocket.Conn, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/cable", &websocket.DialOptions{
		Subprotocols:    []string{"actioncable-v1-json"},
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader:      http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	var welcome map[string]any
	if err := wsjson.Read(ctx, conn, &welcome); err != nil || welcome["type"] != "welcome" {
		t.Fatalf("welcome: %v %v", welcome, err)
	}
	identifier := `{"channel":"UnreadRoomsChannel"}`
	if err := wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
		t.Fatal(err)
	}
	for {
		var got map[string]any
		if err := wsjson.Read(ctx, conn, &got); err != nil {
			t.Fatal(err)
		}
		if got["type"] == "confirm_subscription" {
			return conn, identifier
		}
	}
}

// readUnreadFrame reads until the unread bump for room arrives, skipping the
// protocol frames (welcome, confirm_subscription, ping).
func readUnreadFrame(t *testing.T, conn *websocket.Conn, identifier string, room int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var frame struct {
			Type       string          `json:"type"`
			Identifier string          `json:"identifier"`
			Message    json.RawMessage `json:"message"`
		}
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatalf("reading the unread frame: %v", err)
		}
		if frame.Type != "" {
			continue
		}
		if frame.Identifier != identifier {
			t.Fatalf("unread frame identifier = %q, want %q", frame.Identifier, identifier)
		}
		if want := fmt.Sprintf(`{"roomId":%d}`, room); string(frame.Message) != want {
			t.Fatalf("unread frame payload = %q, want %q", frame.Message, want)
		}
		return
	}
}

// TestNotifyStagesKeepIndependentBudgets pins the shared-deadline fix: a room
// broadcast that spends its whole budget (simulated here as elapsed time —
// the stages take no caller deadline, which is the fix) leaves the unread bump
// and the webhook candidate lookup with their own full budgets, so both still
// run. The webhook is observed as the delivered job, the unread bump on a real
// /cable socket.
func TestNotifyStagesKeepIndependentBudgets(t *testing.T) {
	prev := deliveryTimeout
	deliveryTimeout = 40 * time.Millisecond
	t.Cleanup(func() { deliveryTimeout = prev })

	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// 204 with no content type: the delivery counts, and the bot sends no
		// reply, so the job's own follow-up work stays out of this test.
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	endpoint := webhook.URL
	bot, err := app.DB.CreateUser(ctx, "Budget Bot", "", "", "", 2, &endpoint)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("rooms: %v %v", rooms, err)
	}
	room := rooms[0]
	message, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "hello", "<p>hello</p>", "hello")
	if err != nil {
		t.Fatal(err)
	}
	conn, identifier := cableUnreadSubscriber(t, server, cookie)

	// The earlier stage spent its whole budget three times over.
	time.Sleep(3 * deliveryTimeout)

	// The webhook stage still finds its candidate and the job still delivers.
	app.enqueueWebhooks(message, room, []int64{bot.ID})
	app.Jobs.Close(5 * time.Second)
	if got := calls.Load(); got != 1 {
		t.Fatalf("webhook deliveries after a spent earlier stage = %d, want 1", got)
	}

	// The unread stage still bumps the subscriber.
	app.messageCreated(message, room, nil)
	readUnreadFrame(t, conn, identifier, room.ID)
}
