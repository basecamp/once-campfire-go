package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"golang.org/x/crypto/bcrypt"
)

const formType = "application/x-www-form-urlencoded"

func TestFirstRunSetsUpTheAccountOnce(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("first-run-tests")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)

	response, _ := perform(t, server, "GET", "/session/new", "", nil, nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/first_run" {
		t.Fatal("sign in before the first user", response.Status, response.Header)
	}
	response, body := perform(t, server, "GET", "/first_run", "", nil, nil)
	if response.StatusCode != 200 || !strings.Contains(string(body), "<title>Set up Campfire</title>") || !strings.Contains(string(body), `<body class="signup"`) {
		t.Fatal(response.Status, string(body))
	}
	response, _ = perform(t, server, "POST", "/first_run", formType, strings.NewReader("x=1"), nil)
	if response.StatusCode != 400 {
		t.Fatal("first run without user params", response.Status)
	}

	form := url.Values{"user[name]": {"Ada Lovelace"}, "user[email_address]": {"ada@example.com"}, "user[password]": {"engines"}}
	response, _ = perform(t, server, "POST", "/first_run", formType, strings.NewReader(form.Encode()), nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/" {
		t.Fatal("first run", response.Status, response.Header)
	}
	var cookie *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == "session_token" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("first run didn't sign in")
	}

	ctx := context.Background()
	account, found, err := db.AccountFirst(ctx)
	if err != nil || !found || account.Name != "Campfire" || string(account.Settings) != `{"restrict_room_creation_to_administrators":false}` {
		t.Fatal(account, found, err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{4}-[A-Za-z0-9]{4}-[A-Za-z0-9]{4}$`).MatchString(account.JoinCode) {
		t.Fatal("join code", account.JoinCode)
	}
	administrator, found, err := db.UserFindActiveByEmailAddress(ctx, "ada@example.com")
	if err != nil || !found || administrator.Name != "Ada Lovelace" || administrator.Role != 1 || bcrypt.CompareHashAndPassword([]byte(administrator.Password), []byte("engines")) != nil {
		t.Fatal(administrator, found, err)
	}
	room, found, err := db.LastRoomForUser(ctx, administrator.ID)
	if err != nil || !found || room.Name.String != "All Talk" || room.Type != "Rooms::Open" || room.CreatorID != administrator.ID {
		t.Fatal("first room", room, found, err)
	}

	response, _ = perform(t, server, "GET", "/", "", nil, cookie)
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/rooms/"+strconv.FormatInt(room.ID, 10) {
		t.Fatal("root after first run", response.Status, response.Header)
	}
	for _, method := range []string{"GET", "POST"} {
		response, _ = perform(t, server, method, "/first_run", formType, strings.NewReader(form.Encode()), nil)
		if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/" {
			t.Fatal("repeated first run", method, response.Status)
		}
	}
	if accounts, err := db.AccountCount(ctx); err != nil || accounts != 1 {
		t.Fatal(accounts, err)
	}
}

func TestSignInRejectsThenRateLimits(t *testing.T) {
	app, server, _, user := testApp(t)
	digest, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err := app.DB.UpdateUser(context.Background(), user.ID, map[string]string{"password_digest": string(digest)}, nil); err != nil {
		t.Fatal(err)
	}
	signIn := func(password string) (*http.Response, string) {
		form := url.Values{"email_address": {user.Email}, "password": {password}}
		response, body := perform(t, server, "POST", "/session", formType, strings.NewReader(form.Encode()), nil)
		return response, string(body)
	}
	response, body := signIn("wrong")
	if response.StatusCode != 401 || !strings.Contains(body, `<div class="panel shake">`) || !strings.Contains(body, "Too many requests or unauthorized.") ||
		!strings.Contains(body, `value="`+user.Email+`"`) {
		t.Fatal(response.Status, body)
	}
	response, _ = signIn("")
	if response.StatusCode != 401 {
		t.Fatal("blank password", response.Status)
	}
	if response, _ = signIn("correct horse"); response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/" {
		t.Fatal("sign in", response.Status, response.Header)
	}
	for range 7 {
		signIn("wrong")
	}
	response, body = signIn("correct horse")
	if response.StatusCode != 429 || !strings.Contains(body, "Too many requests or unauthorized.") {
		t.Fatal("11th sign in", response.Status)
	}
	app.attemptsMu.Lock()
	for key, attempt := range app.attempts {
		attempt.Start = attempt.Start.Add(-3 * time.Minute)
		app.attempts[key] = attempt
	}
	app.attemptsMu.Unlock()
	if response, _ = signIn("correct horse"); response.StatusCode != 302 {
		t.Fatal("after the window", response.Status)
	}
}

func TestSessionTransfersSignInOnceValid(t *testing.T) {
	app, server, _, user := testApp(t)
	valid := app.Secrets.SignedID("User", user.ID, "transfer", app.DB.Now().Add(4*time.Hour))
	expired := app.Secrets.SignedID("User", user.ID, "transfer", app.DB.Now().Add(-time.Hour))

	response, body := perform(t, server, "GET", "/session/transfers/"+valid, "", nil, nil)
	if response.StatusCode != 200 || !strings.Contains(string(body), `<form data-controller="auto-submit" action="/session/transfers/`+valid+`" accept-charset="UTF-8" method="post"><input type="hidden" name="_method" value="put" />`) {
		t.Fatal(response.Status, string(body))
	}
	response, body = perform(t, server, "PUT", "/session/transfers/"+expired, formType, strings.NewReader("x=1"), nil)
	if response.StatusCode != 400 || len(body) != 0 {
		t.Fatal("expired transfer", response.Status, string(body))
	}
	response, _ = perform(t, server, "PATCH", "/session/transfers/"+valid, formType, strings.NewReader("x=1"), nil)
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/" {
		t.Fatal("transfer", response.Status, response.Header)
	}
	signedIn := false
	for _, c := range response.Cookies() {
		signedIn = signedIn || c.Name == "session_token"
	}
	if !signedIn {
		t.Fatal("transfer didn't sign in")
	}
}

func TestSignOutRemovesTheSessionAndPushSubscription(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	if _, err := app.DB.Write.Exec("INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key,created_at,updated_at) VALUES (?,?,?,?,?,?)", user.ID, "https://push.test/1", "k", "a", database.Stamp(app.DB.Now()), database.Stamp(app.DB.Now())); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_method": {"delete"}, "push_subscription_endpoint": {"https://push.test/1"}}
	response, body := perform(t, server, "POST", "/session", formType, strings.NewReader(form.Encode()), cookie)
	if response.StatusCode != 302 || response.Header.Get("Location") != server.URL+"/" || len(body) != 0 {
		t.Fatal(response.Status, response.Header, string(body))
	}
	if !strings.Contains(strings.Join(response.Header.Values("Set-Cookie"), "\n"), "session_token=; path=/; max-age=0") {
		t.Fatal("session cookie kept", response.Header)
	}
	var subscriptions, sessions int
	app.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM push_subscriptions").Scan(&subscriptions)
	app.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessions)
	if subscriptions != 0 || sessions != 0 {
		t.Fatal(subscriptions, sessions)
	}
}

func TestInitialsAvatarIsFreshWhenItsUserIs(t *testing.T) {
	app, server, cookie, user := testApp(t)
	path := "/users/" + app.Secrets.SignedID("User", user.ID, "avatar", time.Time{}) + "/avatar"
	response, body := perform(t, server, "GET", path, "", nil, cookie)
	etag := response.Header.Get("ETag")
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "image/svg+xml; charset=utf-8" || !strings.Contains(string(body), "\n      O\n") ||
		response.Header.Get("Cache-Control") != "max-age=1800, public, stale-while-revalidate=604800" || etag == "" || response.Header.Get("X-Frame-Options") != "" {
		t.Fatal(response.Status, response.Header, string(body))
	}
	request, _ := http.NewRequest("GET", server.URL+path, nil)
	request.AddCookie(cookie)
	request.Header.Set("Accept", "*/*")
	request.Header.Set("If-None-Match", etag)
	fresh, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Body.Close()
	if fresh.StatusCode != 304 {
		t.Fatal("not fresh", fresh.Status)
	}
	response, body = perform(t, server, "GET", "/users/not-a-token/avatar", "", nil, cookie)
	if response.StatusCode != 404 || len(body) != 0 {
		t.Fatal(response.Status, string(body))
	}
}
