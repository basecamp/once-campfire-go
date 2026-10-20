package cable

import (
	"context"
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

func TestStreamBatchKeepsRoutingAndFreshSessionAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := database.Open(filepath.Join(t.TempDir(), "publication.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, owner.ID, database.UserInput{Name: "Other", Email: "other@test", Password: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	users := []database.User{owner.User, other.User}
	tokens := make([]string, len(users))
	for i, user := range users {
		tokens[i], err = db.StartSession(ctx, user.ID, "test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
	}
	secrets, err := rails.NewSecrets("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	hub := New(db, secrets)
	defer hub.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := 0
		if r.URL.Path == "/other" {
			i = 1
		}
		hub.Serve(w, r, users[i], tokens[i])
	}))
	defer server.Close()
	read := func(conn *websocket.Conn) map[string]any {
		t.Helper()
		for {
			var frame map[string]any
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				t.Fatal(err)
			}
			if frame["type"] != "ping" {
				return frame
			}
		}
	}
	identifier := `{"channel":"UnreadRoomsChannel"}`
	dial := func(path string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		if got := read(conn); got["type"] != "welcome" {
			t.Fatal(got)
		}
		if err := wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
			t.Fatal(err)
		}
		if got := read(conn); got["type"] != "confirm_subscription" {
			t.Fatal(got)
		}
		return conn
	}
	a, b := dial("/"), dial("/other")
	first, second := fmt.Sprintf("user_%d_unreads", owner.ID), fmt.Sprintf("user_%d_unreads", other.ID)
	check := func(conn *websocket.Conn, message string) {
		t.Helper()
		if got := read(conn); got["identifier"] != identifier || got["message"] != message {
			t.Fatalf("stream routing: %v, want %q", got, message)
		}
	}
	hub.PublishStreams(ctx, "both", first, second, first)
	check(a, "both")
	check(b, "both")
	// Ordered markers detect duplicate delivery and cross-user leakage without
	// timing-based assertions that a socket has received nothing.
	hub.PublishStreams(ctx, "only owner", first)
	hub.PublishStreams(ctx, "only other", second)
	check(a, "only owner")
	check(b, "only other")
	if err := db.RevokeSession(ctx, other.ID, tokens[1], ""); err != nil {
		t.Fatal(err)
	}
	hub.PublishStreams(ctx, "after revocation", first, second)
	check(a, "after revocation")
	var frame map[string]any
	if err := wsjson.Read(ctx, b, &frame); err == nil {
		t.Fatal("revoked session received a batch frame", frame)
	}
}
