package web

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/storage"
)

func TestFormDirectUpload(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	content := []byte("0123456789")
	sum := md5.Sum(content)
	fields := url.Values{
		"blob[filename]": {"audit.png"}, "blob[content_type]": {"image/png"},
		"blob[byte_size]": {strconv.Itoa(len(content))},
		"blob[checksum]":  {base64.StdEncoding.EncodeToString(sum[:])},
	}
	response, data := perform(t, server, "POST", "/rails/active_storage/direct_uploads",
		"application/x-www-form-urlencoded", strings.NewReader(fields.Encode()), cookie)
	if response.StatusCode != 200 {
		t.Fatalf("form allocation: %s %s", response.Status, data)
	}
	var allocation struct {
		ID     int64
		Direct struct{ URL string } `json:"direct_upload"`
	}
	if err := json.Unmarshal(data, &allocation); err != nil {
		t.Fatal(err)
	}
	blob, err := app.DB.Blob(context.Background(), allocation.ID)
	if err != nil {
		t.Fatal(err)
	}
	proxy := strings.Replace(storage.BlobURL(app.Storage.Verifier, blob), "/redirect/", "/proxy/", 1)
	response, _ = perform(t, server, "GET", proxy, "", nil, nil)
	if response.StatusCode != 404 || response.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("unwritten upload cached: %s %v", response.Status, response.Header)
	}
	response, _ = perform(
		t,
		server,
		"PUT",
		allocation.Direct.URL,
		"image/png",
		bytes.NewReader(content),
		cookie,
	)
	if response.StatusCode != 204 {
		t.Fatal(response.Status)
	}
	response, data = perform(t, server, "GET", proxy, "", nil, nil)
	if response.StatusCode != 200 || !bytes.Equal(data, content) ||
		!strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("uploaded bytes unavailable: %s %v %q", response.Status, response.Header, data)
	}
}

func TestBlobProxyControllerPolicy(t *testing.T) {
	app, server, _, _ := testApp(t)
	ctx := context.Background()
	blob, err := app.Storage.Stage(ctx, "audit.png", "image/png", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := strings.Replace(
		storage.BlobURL(app.Storage.Verifier, blob),
		"/redirect/",
		"/proxy/",
		1,
	) + "?disposition=attachment"
	response, data := storageResponse(t, server, "GET", proxy, http.Header{"Range": {"bytes=1-2"}})
	if response.StatusCode != 206 || string(data) != "12" ||
		response.Header.Get("Content-Range") != "bytes 1-2/10" ||
		!strings.HasPrefix(response.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("blob range: %s %v %q", response.Status, response.Header, data)
	}
	head, body := storageResponse(t, server, "HEAD", proxy, http.Header{"Range": {"bytes=1-2"}})
	if head.StatusCode != response.StatusCode || len(body) != 0 ||
		head.Header.Get("Content-Range") != response.Header.Get("Content-Range") ||
		head.ContentLength != response.ContentLength {
		t.Fatalf("range HEAD: %s %v %q", head.Status, head.Header, body)
	}
	response, data = storageResponse(
		t,
		server,
		"GET",
		proxy,
		http.Header{"Range": {"bytes=0-1,3-4"}},
	)
	_, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || response.StatusCode != 206 || response.ContentLength != int64(len(data)) {
		t.Fatalf("multipart: %s %v %v", response.Status, response.Header, err)
	}
	parts := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	for i, want := range []string{"01", "34"} {
		part, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(part)
		if err != nil || string(got) != want ||
			part.Header.Get("Content-Range") != []string{"bytes 0-1/10", "bytes 3-4/10"}[i] {
			t.Fatalf("multipart bytes: %q %v %v", got, part.Header, err)
		}
	}
	if _, err := parts.NextPart(); err != io.EOF {
		t.Fatalf("multipart termination: %v", err)
	}
	response, data = storageResponse(t, server, "GET", proxy, http.Header{"Range": {"bytes=99-"}})
	if response.StatusCode != 416 || len(data) != 0 {
		t.Fatalf("unsatisfiable blob: %s %q", response.Status, data)
	}
}

func TestRepresentationProxyControllerPolicy(t *testing.T) {
	app, server, _, _ := testApp(t)
	ctx := context.Background()
	source, err := os.Open("../../reference/reference/test/fixtures/files/moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	image, err := app.Storage.Stage(ctx, "moon.jpg", "image/jpeg", source)
	if err != nil {
		t.Fatal(err)
	}
	variation := storage.Resize(128, 128, "webp")
	path, err := storage.RepresentationURL(app.Storage.Verifier, image, variation)
	if err != nil {
		t.Fatal(err)
	}
	path = strings.Replace(path, "/redirect/", "/proxy/", 1) + "?disposition=attachment"
	response, full := storageResponse(t, server, "GET", path, nil)
	if response.StatusCode != 200 ||
		!strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") ||
		response.Header.Get("Accept-Ranges") != "" {
		t.Errorf("representation disposition: %s %v", response.Status, response.Header)
	}
	ranged, body := storageResponse(t, server, "GET", path, http.Header{"Range": {"bytes=0-1"}})
	if ranged.StatusCode != 200 || !bytes.Equal(body, full) ||
		ranged.Header.Get("Content-Range") != "" ||
		ranged.ContentLength != int64(len(full)) {
		t.Errorf(
			"representation was ranged: %s %v (%d bytes)",
			ranged.Status,
			ranged.Header,
			len(body),
		)
	}
	response, body = storageResponse(
		t,
		server,
		"GET",
		path,
		http.Header{"Range": {"bytes=99-"}, "If-None-Match": {response.Header.Get("ETag")}},
	)
	if response.StatusCode != 304 || len(body) != 0 {
		t.Fatalf("representation validator ignored: %s %q", response.Status, body)
	}
}

func storageResponse(
	t *testing.T,
	server *httptest.Server,
	method, path string,
	headers http.Header,
) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, values := range headers {
		request.Header[key] = values
	}
	response, err := server.Client().Do(request)
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
