package web

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAccountMutationRechecksCapturedAdministrator(t *testing.T) {
	app, _, _, captured := testApp(t)
	if _, err := app.DB.Write.Exec("UPDATE users SET role=0 WHERE id=?", captured.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PATCH", "/account", strings.NewReader("account%5Bname%5D=unauthorized"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	writer, request := app.withBrowserSession(w, app.normalizeRequest(r))
	app.updateAccount(writer, request, captured)
	if w.Code != http.StatusForbidden {
		t.Fatalf("stale administrator mutated account: %d", w.Code)
	}
	account, err := app.DB.Account(context.Background())
	if err != nil || account.Name != "Campfire" {
		t.Fatalf("unauthorized account: %+v %v", account, err)
	}
}

func TestLogoutRollsBackSessionWhenSubscriptionRemovalFails(t *testing.T) {
	app, server, cookie, user := testApp(t)
	endpoint := "https://push.example.test/owned"
	if _, err := app.DB.Write.Exec("INSERT INTO push_subscriptions(user_id,endpoint,created_at,updated_at) VALUES (?,?,?,?)", user.ID, endpoint, app.DB.Now(), app.DB.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Write.Exec(`CREATE TRIGGER reject_subscription_removal BEFORE DELETE ON push_subscriptions BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	response, _ := perform(t, server, "DELETE", "/session", "application/x-www-form-urlencoded", strings.NewReader(url.Values{"push_subscription_endpoint": {endpoint}}.Encode()), cookie)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatal(response.Status)
	}
	var sessions, subscriptions int
	if err := app.DB.Read.QueryRow("SELECT count(*) FROM sessions WHERE user_id=?", user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Read.QueryRow("SELECT count(*) FROM push_subscriptions WHERE user_id=?", user.ID).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || subscriptions != 1 {
		t.Fatalf("partially revoked session: sessions=%d subscriptions=%d", sessions, subscriptions)
	}
}

func TestProfileCommitKeepsRedirectWhenAnalysisIsRejected(t *testing.T) {
	app, server, cookie, user := testApp(t)
	app.Jobs.Close(time.Second)
	source, err := os.ReadFile("../../reference/reference/test/fixtures/files/moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("user[avatar]", "moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(source); err != nil {
		t.Fatal(err)
	}
	if err = form.WriteField("user[name]", "Committed profile"); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	response, raw := perform(t, server, "PATCH", "/users/me/profile", form.FormDataContentType(), &body, cookie)
	if response.StatusCode != http.StatusFound {
		t.Fatalf("committed profile lost redirect: %s %s", response.Status, raw)
	}
	stored, err := app.DB.User(context.Background(), user.ID)
	if err != nil || stored.Name != "Committed profile" {
		t.Fatal(stored, err)
	}
	blob, err := app.DB.AttachedBlob(context.Background(), "User", user.ID, "avatar")
	if err != nil {
		t.Fatal(err)
	}
	path, err := app.Storage.Path(blob.Key)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, source) {
		t.Fatal(fmt.Sprintf("committed avatar bytes lost: %v", err))
	}
}
