package cable

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type presenceFixture struct {
	ctx        context.Context
	db         *database.DB
	hub        *Hub
	server     *httptest.Server
	user       database.User
	room       int64
	identifier string
	finished   chan struct{}
}

func newPresenceFixture(t *testing.T) presenceFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	db, err := database.Open(filepath.Join(t.TempDir(), "presence.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	hub := New(db, secrets)
	t.Cleanup(hub.Close)
	finished := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(w, r, user.User, token)
		finished <- struct{}{}
	}))
	t.Cleanup(server.Close)
	return presenceFixture{ctx: ctx, db: db, hub: hub, server: server, user: user.User, room: rooms[0].ID, identifier: fmt.Sprintf(`{"channel":"PresenceChannel","room_id":%d}`, rooms[0].ID), finished: finished}
}

func (f presenceFixture) connect(t *testing.T) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(f.ctx, "ws"+strings.TrimPrefix(f.server.URL, "http"), &websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	var frame map[string]any
	if err := wsjson.Read(f.ctx, conn, &frame); err != nil || frame["type"] != "welcome" {
		t.Fatalf("welcome: %v %v", frame, err)
	}
	f.confirm(t, conn)
	return conn
}

func (f presenceFixture) confirm(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := wsjson.Write(f.ctx, conn, map[string]string{"command": "subscribe", "identifier": f.identifier}); err != nil {
		t.Fatal(err)
	}
	for {
		var frame map[string]any
		if err := wsjson.Read(f.ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "ping" {
			continue
		}
		if frame["type"] != "confirm_subscription" || frame["identifier"] != f.identifier {
			t.Fatalf("presence admission: %v", frame)
		}
		return
	}
}

func (f presenceFixture) count(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.Read.QueryRowContext(f.ctx, "SELECT connections FROM memberships WHERE user_id=? AND room_id=?", f.user.ID, f.room).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRepeatedPresentClearsUnreadWithoutClaimingAnotherConnection(t *testing.T) {
	f := newPresenceFixture(t)
	conn := f.connect(t)
	if _, err := f.db.Write.ExecContext(f.ctx, "UPDATE memberships SET unread_at=? WHERE user_id=? AND room_id=?", database.Stamp(f.db.Now()), f.user.ID, f.room); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"refresh", "present", "present"} {
		if err := wsjson.Write(f.ctx, conn, map[string]string{"command": "message", "identifier": f.identifier, "data": fmt.Sprintf(`{"action":%q}`, action)}); err != nil {
			t.Fatal(err)
		}
		f.confirm(t, conn) // Ordered protocol barrier after the action.
		var unread sql.NullString
		if err := f.db.Read.QueryRowContext(f.ctx, "SELECT unread_at FROM memberships WHERE user_id=? AND room_id=?", f.user.ID, f.room).Scan(&unread); err != nil {
			t.Fatal(err)
		}
		if want := action == "refresh"; unread.Valid != want {
			t.Fatalf("%s: unread=%t, want %t", action, unread.Valid, want)
		}
		if got := f.count(t); got != 1 {
			t.Fatalf("%s claimed another presence connection: %d", action, got)
		}
	}
}

func TestCancelledUnsubscribeRetainsPresenceCleanup(t *testing.T) {
	f := newPresenceFixture(t)
	conn := f.connect(t)
	tx, err := f.db.Write.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := f.db.Write.Stats().WaitCount
	if err := wsjson.Write(f.ctx, conn, map[string]string{"command": "unsubscribe", "identifier": f.identifier}); err != nil {
		t.Fatal(err)
	}
	for f.db.Write.Stats().WaitCount == before {
		select {
		case <-f.ctx.Done():
			t.Fatal("unsubscribe did not queue", f.ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	closed := make(chan struct{})
	go func() { f.hub.Close(); close(closed) }()
	// The cancelled unsubscribe must leave its receipt owned until the
	// cancellation-independent disconnect cleanup acquires the writer.
	for f.db.Write.Stats().WaitCount < before+2 {
		select {
		case <-closed:
			t.Fatal("cancelled unsubscribe lost persisted presence ownership")
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-f.ctx.Done():
		t.Fatal("hub did not join presence cleanup", f.ctx.Err())
	}
	if got := f.count(t); got != 0 {
		t.Fatalf("presence after cancelled unsubscribe: %d", got)
	}
}

func TestAbsentRefreshCannotConsumeAnotherTabsPresence(t *testing.T) {
	f := newPresenceFixture(t)
	a, b := f.connect(t), f.connect(t)
	if got := f.count(t); got != 2 {
		t.Fatalf("two tabs: %d", got)
	}
	action := func(name string) {
		t.Helper()
		if err := wsjson.Write(f.ctx, a, map[string]string{"command": "message", "identifier": f.identifier, "data": fmt.Sprintf(`{"action":%q}`, name)}); err != nil {
			t.Fatal(err)
		}
		f.confirm(t, a) // Ordered protocol barrier after the action.
	}
	action("absent")
	if got := f.count(t); got != 1 {
		t.Fatalf("one absent tab: %d", got)
	}
	action("refresh")
	if got := f.count(t); got != 1 {
		t.Fatalf("refresh claimed an absent tab's presence: %d", got)
	}
	a.CloseNow()
	select {
	case <-f.finished:
	case <-f.ctx.Done():
		t.Fatal("absent tab did not finish disconnect cleanup", f.ctx.Err())
	}
	if got := f.count(t); got != 1 {
		t.Fatalf("disconnect consumed the other tab's presence: %d", got)
	}
	f.confirm(t, b)
	if got := f.count(t); got != 1 {
		t.Fatalf("remaining tab's contribution: %d", got)
	}
}
