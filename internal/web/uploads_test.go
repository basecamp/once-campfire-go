package web

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func testApp(t testing.TB) (*Server, *httptest.Server, *http.Cookie, database.User) {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("http-tests")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(context.Background(), user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)
	return app, server, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user
}
func perform(t *testing.T, server *httptest.Server, method, path, ct string, body io.Reader, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	if !strings.HasPrefix(path, "http") {
		path = server.URL + path
	}
	request, err := http.NewRequest(method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "*/*")
	if ct != "" {
		request.Header.Set("Content-Type", ct)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}
func TestDirectUploadAndSignedDownloads(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	content := "uploaded through Active Storage"
	sum := md5.Sum([]byte(content))
	request := map[string]any{"blob": map[string]any{"filename": "notes.txt", "content_type": "text/plain", "byte_size": len(content), "checksum": base64.StdEncoding.EncodeToString(sum[:])}}
	raw, _ := json.Marshal(request)
	response, _ := perform(t, server, "POST", "/rails/active_storage/direct_uploads", "application/json", bytes.NewReader(raw), nil)
	if response.StatusCode != 401 {
		t.Fatal("unauthenticated direct upload", response.Status)
	}
	response, data := perform(t, server, "POST", "/rails/active_storage/direct_uploads", "application/json", bytes.NewReader(raw), cookie)
	if response.StatusCode != 200 {
		t.Fatalf("create: %s %s", response.Status, data)
	}
	var result struct {
		ID       int64
		SignedID string `json:"signed_id"`
		Direct   struct {
			URL string `json:"url"`
		} `json:"direct_upload"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	response, _ = perform(t, server, "PUT", result.Direct.URL, "text/plain", strings.NewReader(content), cookie)
	if response.StatusCode != 204 {
		t.Fatal("upload", response.Status)
	}
	b, err := app.Storage.Blob(context.Background(), result.ID)
	if err != nil {
		t.Fatal(err)
	}
	response, _ = perform(t, server, "GET", app.Storage.BlobURL(b), "", nil, nil)
	if response.StatusCode != 302 {
		t.Fatal("blob redirect", response.Status)
	}
	response, data = perform(t, server, "GET", response.Header.Get("Location"), "", nil, nil)
	if response.StatusCode != 200 || string(data) != content {
		t.Fatalf("download: %s %q", response.Status, data)
	}
	if response.Header.Get("Content-Disposition") != storage.Disposition("attachment", "notes.txt") {
		t.Fatal(response.Header)
	}
	altered := strings.Replace(app.Storage.BlobURL(b), result.SignedID, result.SignedID+"bad", 1)
	response, _ = perform(t, server, "GET", altered, "", nil, nil)
	if response.StatusCode != 404 {
		t.Fatal("tampered signature accepted", response.Status)
	}
}
func TestMessageImageUploadAndVariant(t *testing.T) {
	app, server, cookie, user := testApp(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../reference/reference/test/fixtures/files/moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("message[attachment]", "moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(source)
	form.WriteField("message[client_message_id]", "test-image")
	form.Close()
	path := fmt.Sprintf("/rooms/%d/messages", rooms[0].ID)
	response, data := perform(t, server, "POST", path, form.FormDataContentType(), &body, cookie)
	if response.StatusCode != 200 {
		t.Fatalf("message upload: %s %s", response.Status, data)
	}
	messages, err := app.DB.Messages(context.Background(), rooms[0].ID, 0)
	if err != nil || len(messages) != 1 {
		t.Fatal(messages, err)
	}
	blob, err := app.Storage.Attached(context.Background(), "Message", messages[0].ID, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	variation := storage.Resize(1200, 800, "")
	preview, err := app.Storage.RepresentationURL(blob, variation)
	if err != nil {
		t.Fatal(err)
	}
	response, data = perform(t, server, "GET", preview, "", nil, nil)
	if response.StatusCode != 302 {
		t.Fatalf("representation: %s %s", response.Status, data)
	}
	response, data = perform(t, server, "GET", response.Header.Get("Location"), "", nil, nil)
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "image/jpeg") || len(data) == 0 {
		t.Fatalf("preview: %s %v", response.Status, response.Header)
	}
	response, data = perform(t, server, "GET", path, "", nil, cookie)
	if response.StatusCode != 200 || !bytes.Contains(data, []byte("data-lightbox-target")) {
		t.Fatalf("attachment markup: %s %s", response.Status, data)
	}
	query := url.Values{"q": {"moon"}}
	response, data = perform(t, server, "GET", "/searches?"+query.Encode(), "", nil, cookie)
	if response.StatusCode != 200 {
		t.Fatalf("search attachment: %s %s", response.Status, data)
	}
}
