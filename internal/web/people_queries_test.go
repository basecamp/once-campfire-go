package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestAutocompleteKeepsPaginationAndRoomScope(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	stamp := database.Stamp(app.DB.Now())
	_, err := app.DB.Write.ExecContext(ctx, `WITH RECURSIVE people(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM people WHERE n<20)
 INSERT INTO users(name,email_address,bio,role,status,created_at,updated_at)
 SELECT printf('Candidate%02d',n),printf('candidate%d@test',n),'',CASE WHEN n=20 THEN 2 ELSE 0 END,0,?,? FROM people`, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (*http.Response, []struct {
		Name  string
		Value int64
	}) {
		t.Helper()
		response, body := perform(t, server, "GET", path, "", nil, cookie)
		var suggestions []struct {
			Name  string
			Value int64
		}
		if response.StatusCode != 200 || response.Header.Get("X-Total-Count") != "21" || json.Unmarshal(body, &suggestions) != nil {
			t.Fatalf("suggestions: %s %s", response.Status, body)
		}
		return response, suggestions
	}
	first, items := get("/autocompletable/users.json")
	if len(items) != 20 || items[19].Name != "Candidate20" || !strings.Contains(first.Header.Get("Link"), "page=2") {
		t.Fatalf("first suggestions lost active bots/next page: %+v %s", items, first.Header.Get("Link"))
	}
	last, items := get("/autocompletable/users.json?page=2")
	if len(items) != 1 || items[0].Value != owner.ID || last.Header.Get("Link") != "" {
		t.Fatalf("last suggestions: %+v %s", items, last.Header.Get("Link"))
	}
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	response, body := perform(t, server, "GET", fmt.Sprintf("/autocompletable/users.json?room_id=%d", rooms[0].ID), "", nil, cookie)
	if response.StatusCode != 200 || response.Header.Get("X-Total-Count") != "1" || !strings.Contains(string(body), `"name":"Owner"`) {
		t.Fatalf("scoped suggestions: %s %s", response.Status, body)
	}
	response, _ = perform(t, server, "GET", "/autocompletable/users.json?room_id=0", "", nil, cookie)
	if response.StatusCode != 404 {
		t.Fatalf("explicit zero bypassed room authority: %s", response.Status)
	}
}

func TestUserProfileKeepsContactAndTransferControls(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	commit, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Subject", Email: "subject@test", Password: "unused", Bio: "Owned bio", Role: 0})
	if err != nil {
		t.Fatal(err)
	}
	subject := commit.User
	response, body := perform(t, server, "GET", fmt.Sprintf("/users/%d", subject.ID), "", nil, cookie)
	if response.StatusCode != 200 || !strings.Contains(string(body), `href="mailto:subject@test"`) || !strings.Contains(string(body), "Owned bio") || !strings.Contains(string(body), "session_transfer_url") {
		t.Fatalf("administrator profile: %s %s", response.Status, body)
	}
	path := app.transferPath(subject.ID)
	token := strings.TrimPrefix(path, "/session/transfers/")
	now := app.DB.Now()
	id, err := app.Secrets.VerifyID("User", token, "transfer", now)
	if err != nil || id != subject.ID {
		t.Fatalf("transfer identity: %d %v", id, err)
	}
	if _, err := app.Secrets.VerifyID("User", token, "transfer", now.Add(4*time.Hour+time.Second)); err == nil {
		t.Fatal("transfer lost four-hour expiry")
	}
	session, err := app.DB.StartSession(ctx, subject.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := app.Secrets.SignCookie("session_token", session, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	response, body = perform(t, server, "GET", fmt.Sprintf("/users/%d", subject.ID), "", nil, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)})
	if response.StatusCode != 200 || strings.Contains(string(body), "mailto:subject@test") || strings.Contains(string(body), "session_transfer_url") {
		t.Fatalf("member profile exposed administrator controls: %s %s", response.Status, body)
	}
}
