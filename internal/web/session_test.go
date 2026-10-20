package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
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
	passwordDigest := string(digest)
	if _, err := app.DB.UpdateUser(ctx, user.ID, user.ID, database.UserChanges{Password: &passwordDigest}); err != nil {
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
	response = request(
		"POST",
		"/session",
		url.Values{"email_address": {user.Email}, "password": {"correct horse"}}.Encode(),
	)
	if response.StatusCode != 302 ||
		response.Header.Get("Location") != server.URL+"/rooms/1?test=return" {
		t.Fatal(response.Status, response.Header)
	}
	response = request("GET", "/rooms/1", "")
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	if response := request("GET", "/session/new", ""); response.StatusCode != 200 {
		t.Fatalf("signed-in login form: %s", response.Status)
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
	room, err := app.DB.CreateRoom(
		ctx,
		user.ID,
		"Rooms::Open",
		&sql.NullString{String: "Second", Valid: true},
		nil,
	)
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

func TestSignedInLoginCanChangeUser(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	digest, err := bcrypt.GenerateFromPassword([]byte("other password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	other, err := app.DB.CreateUser(context.Background(), owner.ID, database.UserInput{Name: "Other", Email: "other@example.test", Password: string(digest), Bio: "", Role: 0, Webhook: nil})
	if err != nil {
		t.Fatal(err)
	}
	body := url.Values{"email_address": {other.Email}, "password": {"other password"}}.Encode()
	response, data := perform(
		t,
		server,
		"POST",
		"/session",
		"application/x-www-form-urlencoded",
		strings.NewReader(body),
		cookie,
	)
	if response.StatusCode != 302 {
		t.Fatalf("sign in as another user: %s %s", response.Status, data)
	}
	var switched *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == "session_token" {
			switched = c
		}
	}
	if switched == nil {
		t.Fatal("authenticated sign-in did not issue the new user's cookie")
	}
	response, data = perform(t, server, "GET", "/users/me/profile", "", nil, switched)
	if response.StatusCode != 200 ||
		!strings.Contains(
			string(data),
			fmt.Sprintf(`<meta name="current-user-id" content="%d">`, other.ID),
		) {
		t.Fatalf(
			"subsequent request did not authenticate the new user: %s %s",
			response.Status,
			data,
		)
	}
}

func TestTransferPageHasOneCompleteAutoSubmitForm(t *testing.T) {
	_, server, _, _ := testApp(t)
	response, body := perform(t, server, http.MethodGet, "/session/transfers/example", "", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("transfer GET: %s", response.Status)
	}
	html := string(body)
	if strings.Count(html, "<form ") != strings.Count(html, "</form>") ||
		strings.Count(html, `data-controller="auto-submit"`) != 1 {
		t.Fatal("transfer response must contain one balanced auto-submit form")
	}
	if !regexp.MustCompile(`<form\b[^>]*data-controller="auto-submit"[^>]*>(?:\s*<input\b[^>]*>)*\s*</form>`).
		MatchString(html) {
		t.Fatal("transfer form must close after its hidden fields")
	}
	if !strings.Contains(html, `action="/session/transfers/example"`) ||
		!strings.Contains(html, `data-controller="auto-submit"`) ||
		!strings.Contains(html, `name="_method" value="put"`) {
		t.Fatal("transfer form must auto-submit PUT to its own URL")
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "session_token" {
			t.Fatal("transfer GET must not establish an authenticated session")
		}
	}
}
