package web

// ENGINE-45b local harness: drives POST /rooms/{id}/messages (the create
// path) end-to-end over httptest with the loadgen's Accept header, on a
// seeded database with production defaults (write queue on, fastdb on, auth
// fast on). Used with go test -bench / -cpuprofile to measure and profile
// the handler CPU the official benchmark attributes to post_message.

import (
	"context"
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

// engine45bFixture builds a server + cookie over a fresh seeded database,
// mirroring the write-lane parity fixture and the production defaults.
func engine45bFixture(t testing.TB) (*httptest.Server, *http.Cookie) {
	t.Helper()
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
	secrets, err := rails.NewSecrets("engine-45b-harness")
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
	token, err := db.StartSession(ctx, user.ID, "harness", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	return server, cookie
}

// engine45bPost performs one create request with the loadgen's exact Accept
// header and reads the response body.
func engine45bPost(server *httptest.Server, cookie *http.Cookie, body string) (int, []byte) {
	request, err := http.NewRequest("POST", server.URL+"/rooms/1/messages", strings.NewReader(body))
	if err != nil {
		panic(err)
	}
	request.Host = "chat.test"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "text/vnd.turbo-stream.html, text/html, application/xhtml+xml")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		panic(err)
	}
	return response.StatusCode, data
}

// TestEngine45bHarnessSanity pins the harness: a turbo-stream Accept must get
// a 200 turbo stream body and persist the message, like the loadgen asserts.
func TestEngine45bHarnessSanity(t *testing.T) {
	server, cookie := engine45bFixture(t)
	form := url.Values{"message[body]": {"hello lane"}, "message[client_message_id]": {"harness-1"}}.Encode()
	status, data := engine45bPost(server, cookie, form)
	if status != 200 {
		t.Fatalf("post status %d body=%s, want 200", status, data)
	}
	if !strings.HasPrefix(string(data), `<turbo-stream action="append"`) {
		t.Fatalf("body not a turbo append stream: %s", data)
	}
	if !strings.Contains(string(data), "hello lane") {
		t.Fatalf("stream body missing message text: %s", data)
	}
}

// BenchmarkEngine45bPost measures one create request end to end. Each
// iteration posts a distinct message (unique client_message_id), so the
// write lane sees real new inserts, exactly like the loadgen.
func BenchmarkEngine45bPost(b *testing.B) {
	server, cookie := engine45bFixture(b)
	var i int64
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		i++
		body := fmt.Sprintf("bench write %d", i)
		form := url.Values{"message[body]": {body}, "message[client_message_id]": {fmt.Sprintf("b45-%d", n)}}.Encode()
		status, data := engine45bPost(server, cookie, form)
		if status != 200 || len(data) == 0 {
			b.Fatalf("post %d: status %d body=%s", n, status, data)
		}
	}
}
