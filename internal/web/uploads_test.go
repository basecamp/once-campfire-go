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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func testApp(t *testing.T) (*testRuntime, *httptest.Server, *http.Cookie, database.User) {
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
	app, err := newTestRuntime(db, secrets, root)
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
	return app, server, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user.User
}
func perform(
	t *testing.T,
	server *httptest.Server,
	method, path, ct string,
	body io.Reader,
	cookie *http.Cookie,
) (*http.Response, []byte) {
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
	request := map[string]any{
		"blob": map[string]any{
			"filename":     "notes.txt",
			"content_type": "text/plain",
			"byte_size":    len(content),
			"checksum":     base64.StdEncoding.EncodeToString(sum[:]),
		},
	}
	raw, _ := json.Marshal(request)
	response, _ := perform(
		t,
		server,
		"POST",
		"/rails/active_storage/direct_uploads",
		"application/json",
		bytes.NewReader(raw),
		nil,
	)
	if response.StatusCode != 401 {
		t.Fatal("unauthenticated direct upload", response.Status)
	}
	response, data := perform(
		t,
		server,
		"POST",
		"/rails/active_storage/direct_uploads",
		"application/json",
		bytes.NewReader(raw),
		cookie,
	)
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
	b, err := app.DB.Blob(context.Background(), result.ID)
	if err != nil {
		t.Fatal(err)
	}
	proxy := strings.Replace(storage.BlobURL(app.Storage.Verifier, b), "/redirect/", "/proxy/", 1)
	response, _ = perform(t, server, "GET", proxy, "", nil, nil)
	if response.StatusCode != 404 || response.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("unwritten upload cached: %s %v", response.Status, response.Header)
	}
	response, _ = perform(
		t,
		server,
		"PUT",
		result.Direct.URL,
		"text/plain",
		strings.NewReader(content),
		cookie,
	)
	if response.StatusCode != 204 {
		t.Fatal("upload", response.Status)
	}
	response, _ = perform(t, server, "GET", storage.BlobURL(app.Storage.Verifier, b), "", nil, nil)
	if response.StatusCode != 302 {
		t.Fatal("blob redirect", response.Status)
	}
	response, data = perform(t, server, "GET", response.Header.Get("Location"), "", nil, nil)
	if response.StatusCode != 200 || string(data) != content {
		t.Fatalf("download: %s %q", response.Status, data)
	}
	if response.Header.Get(
		"Content-Disposition",
	) != storage.Disposition(
		"attachment",
		"notes.txt",
	) {
		t.Fatal(response.Header)
	}
	altered := strings.Replace(storage.BlobURL(app.Storage.Verifier, b), result.SignedID, result.SignedID+"bad", 1)
	response, _ = perform(t, server, "GET", altered, "", nil, nil)
	if response.StatusCode != 404 {
		t.Fatal("tampered signature accepted", response.Status)
	}
}

func TestDirectUploadMetadataRoundtrip(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	fields := url.Values{
		"blob[filename]":  {"metadata.txt"},
		"blob[byte_size]": {"0"},
		"blob[checksum]":  {"1B2M2Y8AsgTpgAmY7PhCfg=="},
	}
	// Keep wire order: grouping [] hashes depends on repeated child keys, not
	// the order in which a url.Values map happens to be traversed.
	body := fields.Encode() + "&blob[metadata][custom]=caf%C3%A9+%26+tea" +
		"&blob[metadata][nested][enabled]=true&blob[metadata][nested][count]=42" +
		"&blob[metadata][tags][]=first&blob[metadata][tags][]=second" +
		"&blob[metadata][items][][name]=one&blob[metadata][items][][details][x]=1" +
		"&blob[metadata][items][][details][y]=2&blob[metadata][items][][name]=two" +
		"&blob[metadata][items][][details][x]=3" +
		"&blob[metadata][items][][tags][]=a&blob[metadata][items][][tags][]=b" +
		"&blob[metadata][empty]=&blob[metadata][nothing]&blob[metadata][empty_array][]" +
		"&blob[metadata][numbered][0]=zero&blob[metadata][numbered][1]=one" +
		"&blob[metadata][duplicate]=first&blob[metadata][duplicate]=last"
	want := map[string]any{
		"custom": "café & tea",
		"nested": map[string]any{"enabled": "true", "count": "42"},
		"tags":   []any{"first", "second"},
		"items": []any{
			map[string]any{"name": "one", "details": map[string]any{"x": "1", "y": "2"}},
			map[string]any{
				"name":    "two",
				"details": map[string]any{"x": "3"},
				"tags":    []any{"a", "b"},
			},
		},
		"empty": "", "nothing": nil, "empty_array": []any{},
		"numbered":  map[string]any{"0": "zero", "1": "one"},
		"duplicate": "last",
	}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	for key, values := range fields {
		if err := writer.WriteField(key, values[0]); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{
		{"[name]", "one"}, {"[details][x]", "1"}, {"[details][y]", "2"},
		{"[name]", "two"}, {"[details][x]", "3"}, {"[tags][]", "a"}, {"[tags][]", "b"},
	} {
		if err := writer.WriteField("blob[metadata][items][]"+pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	jsonBody := `{"blob":{"filename":"metadata.txt","byte_size":0,"checksum":"1B2M2Y8AsgTpgAmY7PhCfg==","metadata":{"flag":true,"count":42,"nothing":null,"nested":{"tags":["x",false,null,7]}}}}`
	large := strings.Repeat("x", 10<<20) // Still below the application's 16 MiB body cap.
	var deep any = "value"
	for range 98 { // blob and metadata occupy the first two builder levels.
		deep = map[string]any{"x": deep}
	}
	for _, tc := range []struct {
		name, contentType, body, query, suffix string
		want                                   map[string]any
	}{
		{"form", "application/x-www-form-urlencoded", body, "", "", want},
		{"multipart/ordered hashes", writer.FormDataContentType(), multipartBody.String(), "", "", map[string]any{"items": want["items"]}},
		{
			"form/large", "application/x-www-form-urlencoded",
			fields.Encode() + "&blob[metadata][large]=" + large, "", "",
			map[string]any{"large": large},
		},
		{"form/format", "application/x-www-form-urlencoded", body, "", ".json", want},
		{
			"form/depth boundary", "application/x-www-form-urlencoded",
			fields.Encode() + "&blob[metadata]" + strings.Repeat("[x]", 98) + "=value",
			"", "", deep.(map[string]any),
		},
		{
			"form/non-hash metadata", "application/x-www-form-urlencoded",
			fields.Encode() + "&blob[metadata][]=ignored", "", "",
			map[string]any{},
		},
		{
			"form/query", "application/x-www-form-urlencoded", body,
			fields.Encode() + "&blob[metadata][query]=only", "",
			map[string]any{"query": "only"},
		},
		{
			"json/query", "application/json", jsonBody,
			fields.Encode() + "&blob[metadata][query]=only", "",
			map[string]any{"query": "only"},
		},
		{"json", "application/json", jsonBody, "", "", map[string]any{
			"flag": true, "count": float64(42), "nothing": nil,
			"nested": map[string]any{"tags": []any{"x", false, float64(7)}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, data := perform(
				t,
				server,
				"POST",
				"/rails/active_storage/direct_uploads"+tc.suffix+"?"+tc.query,
				tc.contentType,
				strings.NewReader(tc.body),
				cookie,
			)
			if response.StatusCode != 200 {
				t.Fatalf("create: %s %s", response.Status, data)
			}
			var result struct {
				ID       int64
				Metadata map[string]any
			}
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Metadata, tc.want) {
				t.Fatal("response metadata differs from the expected structure")
			}
			blob, err := app.DB.Blob(context.Background(), result.ID)
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]any
			if err := json.Unmarshal(blob.Metadata, &stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, tc.want) {
				t.Fatal("stored metadata differs from the expected structure")
			}
		})
	}
}

func TestDirectUploadFormValidation(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	valid := "blob[filename]=metadata.txt&blob[byte_size]=0&blob[checksum]=sum"
	for _, tc := range []struct {
		name, body, query string
		status            int
	}{
		{"missing blob", "", "", 400},
		{"blob array", "blob[]=value", "", 400},
		{"missing required field", "blob[filename]=file", "", 422},
		{"structured scalar field", "blob[filename][]=file&blob[byte_size]=0&blob[checksum]=sum", "", 422},
		{"scalar then hash", valid + "&blob[metadata][x]=scalar&blob[metadata][x][y]=nested", "", 400},
		{"array then hash", valid + "&blob[metadata][x][]=scalar&blob[metadata][x][y]=nested", "", 400},
		{"hash then array", valid + "&blob[metadata][x][y]=nested&blob[metadata][x][]=scalar", "", 400},
		{"depth limit", valid + "&blob[metadata]" + strings.Repeat("[x]", 99) + "=deep", "", 400},
		{"query replaces blob", valid, "blob[metadata][only]=query", 422},
		{"allocation size limit", "blob[filename]=file&blob[byte_size]=16777217&blob[checksum]=sum", "", 413},
		{"body limit", valid + "&blob[metadata][x]=" + strings.Repeat("x", MaxBody), "", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before int
			if err := app.DB.Read.QueryRow("SELECT count(*) FROM active_storage_blobs").Scan(&before); err != nil {
				t.Fatal(err)
			}
			response, data := perform(
				t,
				server,
				"POST",
				"/rails/active_storage/direct_uploads?"+tc.query,
				"application/x-www-form-urlencoded",
				strings.NewReader(tc.body),
				cookie,
			)
			if response.StatusCode != tc.status {
				t.Fatalf("status: %s %s; want %d", response.Status, data, tc.status)
			}
			var after int
			if err := app.DB.Read.QueryRow("SELECT count(*) FROM active_storage_blobs").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("invalid request allocated blob: before %d, after %d", before, after)
			}
		})
	}
	response, _ := perform(
		t,
		server,
		"POST",
		"/rails/active_storage/direct_uploads",
		"application/x-www-form-urlencoded",
		strings.NewReader(valid+"&blob[metadata][x]=value"),
		nil,
	)
	if response.StatusCode != 401 {
		t.Fatalf("unauthenticated form upload: %s", response.Status)
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
	messages, err := messageRecords(app.DB, context.Background(), rooms[0].ID, 0)
	if err != nil || len(messages) != 1 {
		t.Fatal(messages, err)
	}
	blob, err := app.DB.AttachedBlob(context.Background(), "Message", messages[0].ID, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	variation := storage.Resize(1200, 800, "")
	preview, err := storage.RepresentationURL(app.Storage.Verifier, blob, variation)
	if err != nil {
		t.Fatal(err)
	}
	response, data = perform(t, server, "GET", preview, "", nil, nil)
	if response.StatusCode != 302 {
		t.Fatalf("representation: %s %s", response.Status, data)
	}
	response, data = perform(t, server, "GET", response.Header.Get("Location"), "", nil, nil)
	if response.StatusCode != 200 ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "image/jpeg") ||
		len(data) == 0 {
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

func TestSignedDiskUploadCapabilityDoesNotRequireFetchMetadata(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	content := "signed upload capability"
	sum := md5.Sum([]byte(content))
	raw, _ := json.Marshal(map[string]any{"blob": map[string]any{"filename": "capability.txt", "content_type": "text/plain", "byte_size": len(content), "checksum": base64.StdEncoding.EncodeToString(sum[:])}})
	response, body := perform(t, server, "POST", "/rails/active_storage/direct_uploads", "application/json", bytes.NewReader(raw), cookie)
	if response.StatusCode != 200 {
		t.Fatalf("create upload: %d %s", response.StatusCode, body)
	}
	var created struct {
		Direct struct {
			URL string `json:"url"`
		} `json:"direct_upload"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	for _, control := range []struct {
		name, data              string
		authenticated, tampered bool
		status                  int
	}{
		{"signed capability", content, true, false, 204},
		{"requires session", content, false, false, 401},
		{"rejects tampered signature", content, true, true, 404},
		{"rejects changed checksum", strings.Repeat("x", len(content)), true, false, 422},
	} {
		t.Run(control.name, func(t *testing.T) {
			url := created.Direct.URL
			if control.tampered {
				url += "bad"
			}
			r := httptest.NewRequest("PUT", url, strings.NewReader(control.data))
			r.Header.Set("Content-Type", "text/plain")
			r.Header.Set("Origin", "https://other.test")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			if control.authenticated {
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != control.status {
				t.Fatalf("got %d want %d: %s", w.Code, control.status, w.Body.String())
			}
		})
	}
}

func messageInput(client, body string) database.MessageInput {
	return database.MessageInput{ClientID: client, Body: &body}
}
