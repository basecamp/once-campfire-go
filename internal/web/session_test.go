package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
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

func TestTransferPageHasOneCompleteAutoSubmitForm(t *testing.T) {
	_, server, _, _ := testApp(t)
	response, body := perform(t, server, http.MethodGet, "/session/transfers/example", "", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("transfer GET: %s", response.Status)
	}
	html := string(body)
	if strings.Count(html, "<form ") != strings.Count(html, "</form>") || strings.Count(html, `data-controller="auto-submit"`) != 1 {
		t.Fatal("transfer response must contain one balanced auto-submit form")
	}
	if !regexp.MustCompile(`<form\b[^>]*data-controller="auto-submit"[^>]*>(?:\s*<input\b[^>]*>)*\s*</form>`).MatchString(html) {
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

// Authentication reads the session and its user in one statement (SessionUserActivity). These
// pin what that read must still see: a commit from another connection (the admin console, a
// second process, a restore) takes effect on the very next request, warm response cache or not.
func TestAuthenticationSeesForeignSessionAndUserChanges(t *testing.T) {
	for _, change := range []struct {
		name   string
		revoke func(*testing.T, *Server, *sql.DB, database.User, string) *http.Cookie
	}{
		{"deleted session", func(t *testing.T, _ *Server, foreign *sql.DB, _ database.User, token string) *http.Cookie {
			execForeign(t, foreign, "DELETE FROM sessions WHERE token=?", token)
			return nil
		}},
		{"session moved to another token", func(t *testing.T, _ *Server, foreign *sql.DB, _ database.User, token string) *http.Cookie {
			execForeign(t, foreign, "UPDATE sessions SET token=? WHERE token=?", token+"-rotated", token)
			return nil
		}},
		{"deactivated user", func(t *testing.T, _ *Server, foreign *sql.DB, user database.User, _ string) *http.Cookie {
			execForeign(t, foreign, "UPDATE users SET status=1 WHERE id=?", user.ID)
			return nil
		}},
		{"banned user", func(t *testing.T, _ *Server, foreign *sql.DB, user database.User, _ string) *http.Cookie {
			execForeign(t, foreign, "UPDATE users SET status=2 WHERE id=?", user.ID)
			return nil
		}},
		{"expired cookie", func(t *testing.T, app *Server, _ *sql.DB, _ database.User, token string) *http.Cookie {
			expired, err := app.Secrets.SignCookie("session_token", token, time.Now().Add(-time.Second))
			if err != nil {
				t.Fatal(err)
			}
			return &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(expired)}
		}},
		{"signed token without a session", func(t *testing.T, app *Server, _ *sql.DB, _ database.User, token string) *http.Cookie {
			missing, err := app.Secrets.SignCookie("session_token", token+"-missing", time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			return &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(missing)}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			app, _, cookie, user := testApp(t)
			rooms, err := app.DB.Rooms(context.Background(), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
			for _, headers := range []map[string]string{nil, {"Accept-Encoding": "gzip"}} {
				cachedRequest(t, app, cookie, "GET", path, headers)
				hits := cacheHits(app)
				if response := cachedRequest(t, app, cookie, "GET", path, headers); response.Code != 200 || cacheHits(app) != hits+1 {
					t.Fatal("page was not a warm response-cache hit before the change", response.Code)
				}
			}
			foreign := foreignWriter(t, app)
			if replacement := change.revoke(t, app, foreign, user, sessionToken(t, app, cookie)); replacement != nil {
				cookie = replacement
			}
			for _, headers := range []map[string]string{nil, {"Accept-Encoding": "gzip"}} {
				hits := cacheHits(app)
				response := cachedRequest(t, app, cookie, "GET", path, headers)
				if response.Code != 302 || !strings.HasSuffix(response.Header().Get("Location"), "/session/new") || cacheHits(app) != hits {
					t.Fatal("rejected session still served", response.Code, response.Header().Get("Location"), cacheHits(app)-hits)
				}
			}
		})
	}
}

// The hourly refresh moves last_active_at, updated_at, user_agent and ip_address and re-signs the
// cookie only once last_active_at is more than an hour old, as RefreshSession always has; reading
// last_active_at together with the user must not make it refresh earlier or more often.
func TestAuthenticationRefreshesSessionOnlyAfterAnHour(t *testing.T) {
	app, _, _, user := testApp(t)
	base := time.Now().UTC().Truncate(time.Second)
	now := base
	app.DB.Now = func() time.Time { return now }
	token, err := app.DB.StartSession(context.Background(), user.ID, "before", "0.0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := app.Secrets.SignCookie("session_token", token, base.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	ip := remoteIP(httptest.NewRequest("GET", "http://cache.test"+path, nil))
	var stored []string
	setActive := func(active time.Time) {
		t.Helper()
		if _, err := app.DB.Write.Exec("UPDATE sessions SET last_active_at=?,updated_at=?,user_agent='before',ip_address='0.0.0.0' WHERE token=?", database.Stamp(active), database.Stamp(active), token); err != nil {
			t.Fatal(err)
		}
		stored = []string{database.Stamp(active), database.Stamp(active), "before", "0.0.0.0"}
	}
	expect := func(step string, refresh bool) {
		t.Helper()
		response := cachedRequest(t, app, cookie, "GET", path, map[string]string{"User-Agent": "refresh agent"})
		if response.Code != 200 {
			t.Fatal(step, response.Code)
		}
		refreshed := false
		for _, c := range response.Result().Cookies() {
			refreshed = refreshed || c.Name == "session_token"
		}
		var lastActive, updated, agent, address string
		if err := app.DB.Write.QueryRow("SELECT last_active_at,updated_at,user_agent,ip_address FROM sessions WHERE token=?", token).Scan(&lastActive, &updated, &agent, &address); err != nil {
			t.Fatal(err)
		}
		if refresh {
			stored = []string{database.Stamp(now), database.Stamp(now), "refresh agent", ip}
		}
		want := stored
		if refreshed != refresh || lastActive != want[0] || updated != want[1] || agent != want[2] || address != want[3] {
			t.Fatalf("%s: cookie refreshed=%v, session=%q want refresh=%v %q", step, refreshed, []string{lastActive, updated, agent, address}, refresh, want)
		}
	}
	setActive(base.Add(-time.Hour))
	expect("exactly an hour old", false)
	setActive(base.Add(-time.Hour - time.Microsecond))
	expect("just over an hour old", true)
	expect("right after a refresh", false)
	now = base.Add(59*time.Minute + 59*time.Second)
	expect("within the hour after a refresh", false)
	now = base.Add(time.Hour)
	expect("an hour after a refresh", false)
	now = base.Add(time.Hour + time.Microsecond)
	expect("over an hour after a refresh", true)
	expect("again at the same instant", false)
}

func sessionToken(t *testing.T, app *Server, cookie *http.Cookie) string {
	t.Helper()
	var token string
	if err := app.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), app.DB.Now(), &token); err != nil {
		t.Fatal(err)
	}
	return token
}
