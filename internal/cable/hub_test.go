package cable

import (
	"context"
	"encoding/json"
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

func TestRoomAuthorizationAndDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(filepath.Join(t.TempDir(), "cable.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.Setup(ctx, "Owner", "owner@test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	now := database.Stamp(time.Now())
	result, err := db.Write.ExecContext(ctx, "INSERT INTO users(name,email_address,status,role,created_at,updated_at) VALUES ('Outsider','outsider@test',0,0,?,?)", now, now)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	hub := New(db, secrets)
	defer hub.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := user.User
		if r.URL.Path == "/stranger" {
			u.ID = stranger
		}
		hub.Serve(w, r, u, token)
	}))
	defer server.Close()
	dial := func(path string) *websocket.Conn {
		t.Helper()
		conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}, CompressionMode: websocket.CompressionNoContextTakeover, CompressionThreshold: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
			t.Fatal("compression was not negotiated", response.Header)
		}
		t.Cleanup(func() { conn.CloseNow() })
		var welcome map[string]any
		if err = wsjson.Read(ctx, conn, &welcome); err != nil || welcome["type"] != "welcome" {
			t.Fatalf("welcome: %v %v", welcome, err)
		}
		return conn
	}
	identifierBytes, err := json.Marshal(map[string]string{"channel": "RoomMessagesChannel", "signed_stream_name": secrets.SignStream(rails.RoomStream(room.Type, room.ID))})
	if err != nil {
		t.Fatal(err)
	}
	identifier := string(identifierBytes)
	subscribe := func(conn *websocket.Conn, want string) {
		t.Helper()
		if err := wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
			t.Fatal(err)
		}
		for {
			var got map[string]any
			if err := wsjson.Read(ctx, conn, &got); err != nil {
				t.Fatal(err)
			}
			if got["type"] == "ping" {
				continue
			}
			if got["type"] != want {
				t.Fatalf("subscription: %v", got)
			}
			break
		}
	}
	owner := dial("/")
	subscribe(owner, "confirm_subscription")
	// A repeated subscription must remain one recipient, not duplicate delivery.
	subscribe(owner, "confirm_subscription")
	alias := " " + identifier
	if err := wsjson.Write(ctx, owner, map[string]string{"command": "subscribe", "identifier": alias}); err != nil {
		t.Fatal(err)
	}
	var confirmed map[string]any
	if err := wsjson.Read(ctx, owner, &confirmed); err != nil || confirmed["identifier"] != alias || confirmed["type"] != "confirm_subscription" {
		t.Fatal("alias subscription failed", confirmed, err)
	}
	duplicate := dial("/")
	subscribe(duplicate, "confirm_subscription")
	outsider := dial("/stranger")
	subscribe(outsider, "reject_subscription")
	expected := `<turbo-stream action="append"><template>` + strings.Repeat("hello ", 100) + `</template></turbo-stream>`
	hub.Publish(ctx, room.ID, expected)
	var frame map[string]any
	delivered := make(map[string]bool)
	for i := 0; i < 2; i++ {
		if err = wsjson.Read(ctx, owner, &frame); err != nil || frame["message"] != expected {
			t.Fatal("incorrect alias delivery", frame, err)
		}
		delivered[frame["identifier"].(string)] = true
	}
	if !delivered[identifier] || !delivered[alias] || len(delivered) != 2 {
		t.Fatal("room aliases were collapsed", delivered)
	}
	if err = wsjson.Read(ctx, duplicate, &frame); err != nil || frame["message"] != expected {
		t.Fatal("shared recipient did not receive frame", err)
	}
	// Removing one alias must leave the other room identifier intact.
	if err = wsjson.Write(ctx, owner, map[string]string{"command": "unsubscribe", "identifier": alias}); err != nil {
		t.Fatal(err)
	}
	readIdentifier := `{"channel":"ReadRoomsChannel"}`
	if err = wsjson.Write(ctx, owner, map[string]string{"command": "subscribe", "identifier": readIdentifier}); err != nil {
		t.Fatal(err)
	}
	if err = wsjson.Read(ctx, owner, &frame); err != nil || frame["type"] != "confirm_subscription" {
		t.Fatal("read stream subscription failed", frame, err)
	}
	hub.Publish(ctx, room.ID, "remaining alias")
	if err = wsjson.Read(ctx, owner, &frame); err != nil || frame["identifier"] != identifier || frame["message"] != "remaining alias" {
		t.Fatal("selective alias removal changed delivery", frame, err)
	}
	if err = wsjson.Read(ctx, duplicate, &frame); err != nil || frame["message"] != "remaining alias" {
		t.Fatal("other socket delivery changed", frame, err)
	}
	// Unsubscription takes effect before the next command's acknowledgement.
	// A following stream frame must not be preceded by stale room delivery.
	if err = wsjson.Write(ctx, owner, map[string]string{"command": "unsubscribe", "identifier": identifier}); err != nil {
		t.Fatal(err)
	}
	if err = wsjson.Write(ctx, owner, map[string]string{"command": "subscribe", "identifier": readIdentifier}); err != nil {
		t.Fatal(err)
	}
	if err = wsjson.Read(ctx, owner, &frame); err != nil || frame["type"] != "confirm_subscription" {
		t.Fatal("read stream subscription failed", frame, err)
	}
	hub.Publish(ctx, room.ID, "only remaining room subscriber")
	if err = wsjson.Read(ctx, duplicate, &frame); err != nil || frame["message"] != "only remaining room subscriber" {
		t.Fatal("remaining room subscription lost delivery", frame, err)
	}
	hub.PublishStreams(ctx, "stream marker", fmt.Sprintf("user_%d_reads", user.ID))
	if err = wsjson.Read(ctx, owner, &frame); err != nil || frame["identifier"] != readIdentifier || frame["message"] != "stream marker" {
		t.Fatal("unsubscribed room or wrong stream received delivery", frame, err)
	}
	subscribe(owner, "confirm_subscription")
	if _, err = db.Write.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=? AND user_id=?", room.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	hub.Publish(ctx, room.ID, "private after revocation")
	if err = wsjson.Read(ctx, owner, &frame); err == nil {
		t.Fatal("revoked subscriber received frame", frame)
	}
	if err = wsjson.Read(ctx, duplicate, &frame); err == nil {
		t.Fatal("revoked duplicate received frame", frame)
	}
	// Both sockets' room/stream index references are released on disconnect.
	deadline := time.Now().Add(time.Second)
	for {
		hub.mu.RLock()
		remaining := len(hub.subscribers)
		hub.mu.RUnlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected subscribers remained indexed")
		}
		time.Sleep(time.Millisecond)
	}
	// A previously issued, correctly signed stream cannot restore revoked access.
	revoked := dial("/")
	subscribe(revoked, "reject_subscription")
}
func TestSlowClientQueueIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{out: make(chan *websocket.PreparedMessage, 2), cancel: cancel}
	if !c.send("one") || !c.send("two") || c.send("three") {
		t.Fatal("queue limit not enforced")
	}
	if ctx.Err() == nil {
		t.Fatal("slow client was not disconnected")
	}
}
