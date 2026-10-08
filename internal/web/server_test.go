package web

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
	"golang.org/x/crypto/bcrypt"
)

func TestAuthenticationAndMessageFlow(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "app.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, err := rails.NewSecrets("test-only-secret")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ts := httptest.NewServer(app)
	defer ts.Close()
	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req := func(method, path, body string, cookie *http.Cookie, origin string) *http.Response {
		t.Helper()
		r, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "*/*")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		return response
	}
	response := req("GET", "/up", "", nil, "")
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(data) != HealthBody {
		t.Fatalf("health: %d %s %v", response.StatusCode, data, err)
	}
	response = req("GET", "/rooms/1", "", nil, "")
	if response.StatusCode != 302 || response.Header.Get("Location") != ts.URL+"/session/new" {
		t.Fatal("unauthenticated room access", response.Status)
	}
	digest, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.Setup(context.Background(), "David", "david@test", string(digest))
	if err != nil {
		t.Fatal(err)
	}
	response = req("POST", "/session", url.Values{"email_address": {"david@test"}, "password": {"correct horse"}}.Encode(), nil, "")
	if response.StatusCode != 302 {
		t.Fatal("login", response.Status)
	}
	var cookie *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == "session_token" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no signed session cookie")
	}
	var token string
	if err = secrets.VerifyCookie(cookie.Name, rails.UnescapeCookie(cookie.Value), time.Now(), &token); err != nil {
		t.Fatal(err)
	}
	rooms, err := db.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d/messages", rooms[0].ID)
	response = req("POST", path, "message%5Bbody%5D=hello", cookie, "https://attacker.test")
	if response.StatusCode != 422 {
		t.Fatal("cross-origin write allowed", response.Status)
	}
	response = req("POST", path, "message%5Bbody%5D=%3Cp%3Ehello%3C%2Fp%3E", cookie, ts.URL)
	if response.StatusCode != 200 {
		t.Fatal("message write", response.Status)
	}
	response = req("GET", path, "", cookie, "")
	data, err = io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(data), "<p>hello</p>") {
		t.Fatalf("messages: %d %s %v", response.StatusCode, data, err)
	}
	response = req("DELETE", "/session", "", cookie, ts.URL)
	if response.StatusCode != 302 {
		t.Fatal("logout", response.Status)
	}
	response = req("GET", path, "", cookie, "")
	if response.StatusCode != 302 {
		t.Fatal("revoked session still usable", response.Status)
	}
}
func TestOriginPolicy(t *testing.T) {
	for _, c := range []struct {
		secure       bool
		site, origin string
		want         bool
	}{{false, "", "", true}, {true, "", "", false}, {true, "same-origin", "https://chat.test", true}, {true, "same-site", "https://evil.test", false}, {false, "cross-site", "", false}, {false, "same-origin", "null", false}} {
		s := &Server{Secure: c.secure}
		r := httptest.NewRequest("POST", "http://chat.test/session", nil)
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := s.browserWriteAllowed(r); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestFetchMetadataCanonicalPolicyAndProxyHTTPS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, scheme := range []string{"http", "https"} {
			for _, site := range []string{"absent", "", "same-origin", "same-site", "cross-site", "none", "bogus", "Same-Origin"} {
				for _, proxy := range []bool{false, true} {
					s := &Server{Secure: secure}
					r := httptest.NewRequest("POST", scheme+"://chat.test/session", nil)
					if proxy {
						r.Header.Set("X-Forwarded-Proto", "https")
						r.Header.Set("X-Forwarded-Host", "public.test")
					}
					if site != "absent" {
						r.Header.Set("Sec-Fetch-Site", site)
					}
					want := site == "same-origin" || site == "same-site" || site == "absent" && !secure && scheme == "http" && !proxy
					if got := s.browserWriteAllowed(r); got != want {
						t.Errorf("secure=%v scheme=%s site=%q proxy=%v: got %v want %v", secure, scheme, site, proxy, got, want)
					}
					for _, origin := range []string{s.origin(r), "null", "", "https://evil.test", s.origin(r) + "/"} {
						r.Header.Set("Origin", origin)
						if got := s.browserWriteAllowed(r); got != (want && origin == s.origin(r)) {
							t.Errorf("origin=%q site=%q scheme=%s secure=%v proxy=%v: %v", origin, site, scheme, secure, proxy, got)
						}
					}
				}
			}
		}
	}
}

func TestFetchMetadataRejectsUnsafeRoutesBeforeSideEffects(t *testing.T) {
	app, _, cookie, _ := testApp(t)
	for _, route := range []struct{ method, path string }{
		{"POST", "/session"}, {"POST", "/first_run"}, {"POST", "/users"},
		{"PATCH", "/session/transfers/invalid"}, {"POST", "/rails/active_storage/direct_uploads"},
		{"PATCH", "/account"}, {"DELETE", "/session"}, {"OPTIONS", "/session"},
	} {
		for _, auth := range []bool{false, true} {
			for _, site := range []string{"cross-site", "none", "invalid", ""} {
				r := httptest.NewRequest(route.method, "http://chat.test"+route.path, strings.NewReader("authenticity_token=old-tab-token"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Sec-Fetch-Site", site)
				if auth {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				app.ServeHTTP(w, r)
				if w.Code != 422 {
					t.Errorf("%s %s auth=%v site=%q: %d", route.method, route.path, auth, site, w.Code)
				}
			}
		}
	}
}

func TestTokenBearingOldTabsStillWriteWithFetchMetadata(t *testing.T) {
	app, _, cookie, user := testApp(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatal(err)
	}
	body := "authenticity_token=old-tab-token&message%5Bbody%5D=old+tab+still+writes"
	r := httptest.NewRequest("POST", fmt.Sprintf("http://chat.test/rooms/%d/messages", rooms[0].ID), strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "*/*")
	r.Header.Set("Origin", "http://chat.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("old tab write: %d %s", w.Code, w.Body.String())
	}
	messages, err := app.DB.Messages(context.Background(), rooms[0].ID, 0)
	if err != nil || len(messages) == 0 {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "old tab still writes") {
		t.Fatal("accepted write must return persisted message")
	}
}

func TestBotKeyPathDoesNotExemptSessionCookieWrites(t *testing.T) {
	app, _, cookie, user := testApp(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", fmt.Sprintf("http://chat.test/rooms/%d/not-a-bot-key/messages", rooms[0].ID), strings.NewReader("body=forged"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 422 {
		t.Fatalf("session cookie on bot route bypassed policy: %d", w.Code)
	}
}
