package web

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"golang.org/x/crypto/bcrypt"
)

func TestEncryptedLoginReturnAndSessionRefresh(t *testing.T) {
	app, server, _, user := testApp(t)
	ctx := context.Background()
	digest, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err := app.DB.UpdateUser(ctx, user.ID, map[string]string{"password_digest": string(digest)}, nil); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := server.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request := func(method, path, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response
	}
	response := request("GET", "/rooms/1?test=return", "")
	if response.StatusCode != 302 {
		t.Fatal(response.Status)
	}
	var state map[string]any
	for _, c := range response.Cookies() {
		if c.Name == browserSessionCookie {
			if err := app.Secrets.DecryptCookie(c.Name, rails.UnescapeCookie(c.Value), app.DB.Now(), &state); err != nil {
				t.Fatal(err)
			}
		}
	}
	if state["return_to_after_authenticating"] != server.URL+"/rooms/1?test=return" {
		t.Fatal(state)
	}
	response = request("POST", "/session", url.Values{"email_address": {user.Email}, "password": {"correct horse"}}.Encode())
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/rooms/1?test=return" {
		t.Fatal(response.Status, response.Header)
	}
	response = request("GET", "/rooms/1", "")
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	for _, c := range response.Cookies() {
		if c.Name == "session_token" || c.Name == browserSessionCookie {
			t.Fatalf("unchanged session rewritten: %s", c.Name)
		}
	}
	if _, err := app.DB.Write.Exec("UPDATE sessions SET last_active_at=?", database.Stamp(app.DB.Now().Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	response = request("GET", "/rooms/1", "")
	refreshed := false
	for _, c := range response.Cookies() {
		refreshed = refreshed || c.Name == "session_token"
	}
	if !refreshed {
		t.Fatal("missing hourly cookie refresh")
	}
	room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Open", "Second", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = room
	response = request("GET", "/rooms/2", "")
	response = request("GET", "/", "")
	if response.Header.Get("Location") != server.URL+"/rooms/2" {
		t.Fatal("last room ignored", response.Header)
	}
	response = request("DELETE", "/session", "")
	if response.StatusCode != 302 {
		t.Fatal(response.Status)
	}
	response = request("GET", "/rooms/1", "")
	if response.StatusCode != 302 {
		t.Fatal("logout did not revoke session")
	}
}
