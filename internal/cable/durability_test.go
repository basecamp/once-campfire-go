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

func TestForeignRevocationBeforePresence(t *testing.T) {
	for _, change := range []string{"DELETE FROM sessions", "UPDATE users SET status=1", "UPDATE users SET name='Renamed'"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "db.sqlite3")
			db, err := database.Open(path, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			u, err := db.Setup(ctx, "Owner", "owner@test", "unused")
			if err != nil {
				t.Fatal(err)
			}
			rooms, err := db.Rooms(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			token, err := db.StartSession(ctx, u.ID, "test", "local")
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Write.ExecContext(ctx, "UPDATE memberships SET connections=0,connected_at=NULL,unread_at='2026-01-01' WHERE user_id=?", u.ID)
			if err != nil {
				t.Fatal(err)
			}
			secrets, _ := rails.NewSecrets("secret")
			h := New(db, secrets)
			h.heartbeat = time.Hour
			defer h.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, u, token) }))
			defer server.Close()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			var frame map[string]any
			if err = wsjson.Read(ctx, c, &frame); err != nil {
				t.Fatal(err)
			}
			foreign, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer foreign.Close()
			if strings.Contains(change, "Renamed") {
				id := fmt.Sprintf(`{"channel":"TypingNotificationsChannel","room_id":%d}`, rooms[0].ID)
				if err = wsjson.Write(ctx, c, map[string]string{"command": "subscribe", "identifier": id}); err != nil {
					t.Fatal(err)
				}
				if err = wsjson.Read(ctx, c, &frame); err != nil {
					t.Fatal(err)
				}
				if _, err = foreign.ExecContext(ctx, change); err != nil {
					t.Fatal(err)
				}
				if err = wsjson.Write(ctx, c, map[string]string{"command": "message", "identifier": id, "data": `{"action":"start"}`}); err != nil {
					t.Fatal(err)
				}
				if err = wsjson.Read(ctx, c, &frame); err != nil {
					t.Fatal(err)
				}
				name := frame["message"].(map[string]any)["user"].(map[string]any)["name"]
				if name != "Renamed" {
					t.Errorf("typing name=%v", name)
				}
				return
			}
			if _, err = foreign.ExecContext(ctx, change); err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf(`{"channel":"PresenceChannel","room_id":%d}`, rooms[0].ID)
			if err = wsjson.Write(ctx, c, map[string]string{"command": "subscribe", "identifier": id}); err != nil {
				t.Fatal(err)
			}
			if err = wsjson.Read(ctx, c, &frame); err != nil {
				t.Fatal(err)
			}
			if frame["type"] != "reject_subscription" {
				t.Errorf("revoked subscription: %v", frame)
			}
			var connections int
			var connected, unread sql.NullString
			if err = db.Read.QueryRowContext(ctx, "SELECT connections,connected_at,unread_at FROM memberships WHERE user_id=? AND room_id=?", u.ID, rooms[0].ID).Scan(&connections, &connected, &unread); err != nil {
				t.Fatal(err)
			}
			if connections != 0 || connected.Valid || unread.String != "2026-01-01" {
				t.Errorf("revoked write: connections=%d connected=%v unread=%v", connections, connected, unread)
			}
		})
	}
}

func TestAuthzEvictionClearsBackingSlots(t *testing.T) {
	c := newAuthzCache()
	for i := 0; i < authzCacheMax+1; i++ {
		c.store(authzKey{token: fmt.Sprint(i)}, 1, 1, 1, 1, 1)
	}
	if c.ring[0] != (authzKey{}) {
		t.Error("evicted slot retains token")
	}
	for i := authzCacheMax + 1; i < 2*authzCacheMax; i++ {
		c.store(authzKey{token: fmt.Sprint(i)}, 1, 1, 1, 1, 1)
	}
	for i, k := range c.ring[len(c.ring):cap(c.ring)] {
		if k != (authzKey{}) {
			t.Fatalf("compacted tail slot %d retains token", i)
		}
	}
}
