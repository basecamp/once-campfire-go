package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/front"
)

// Through front's Deflate, a gzipped page built from cached blocks decodes to the bytes
// an identity client gets, on the request that fills the cache and on those that hit it.
func TestSplicedGzipMatchesIdentity(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	for i := range 40 {
		body := fmt.Sprintf("<p>spliced message %d %s</p>", i, strings.Repeat("campfire ", i))
		message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("spliced-%d", i), body, "spliced message")
		if err != nil {
			t.Fatal(err)
		}
		last = message.ID
	}
	handler := front.Deflate(app)
	loaded := regexp.MustCompile(`loaded-at-value="\d+"`)
	get := func(path, encoding string) (*httptest.ResponseRecorder, string) {
		t.Helper()
		request := httptest.NewRequest("GET", path, nil)
		request.AddCookie(cookie)
		request.Header.Set("Accept-Encoding", encoding)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatal(path, response.Code, response.Body.String())
		}
		var body io.Reader = response.Body
		size := response.Body.Len()
		if encoding == "gzip" {
			if response.Header().Get("Content-Encoding") != "gzip" {
				t.Fatal(path, response.Header())
			}
			if body, err = gzip.NewReader(body); err != nil {
				t.Fatal(path, err)
			}
		}
		decoded, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(path, err)
		}
		if encoding == "gzip" && size >= len(decoded) {
			t.Fatal(path, "not compressed", size, len(decoded))
		}
		return response, loaded.ReplaceAllString(string(decoded), "")
	}
	paths := map[string]string{
		"room":     fmt.Sprintf("/rooms/%d", rooms[0].ID),
		"messages": fmt.Sprintf("/rooms/%d/messages?before=%d", rooms[0].ID, last),
		"sidebar":  "/users/sidebar",
		"search":   "/searches?q=spliced",
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			// A cold cache for each encoding, then hits.
			app.fragments = newFragmentCache(32 << 20)
			filling, first := get(path, "gzip")
			hit, second := get(path, "gzip")
			app.fragments = newFragmentCache(32 << 20)
			_, plain := get(path, "identity")
			cached, again := get(path, "identity")
			if !strings.Contains(plain, "spliced message") && name != "sidebar" {
				t.Fatal("page has no messages")
			}
			if first != plain || second != plain || again != plain {
				t.Fatal("gzipped page differs from identity page")
			}
			if name != "room" && (filling.Header().Get("ETag") == "" || filling.Header().Get("ETag") != hit.Header().Get("ETag") || hit.Header().Get("ETag") != cached.Header().Get("ETag")) {
				t.Fatal("validator changed between cache fill and hit", filling.Header().Get("ETag"), hit.Header().Get("ETag"), cached.Header().Get("ETag"))
			}
			request := httptest.NewRequest("GET", path, nil)
			request.AddCookie(cookie)
			request.Header.Set("Accept-Encoding", "gzip")
			request.Header.Set("If-None-Match", hit.Header().Get("ETag"))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if name != "room" && (response.Code != http.StatusNotModified || response.Body.Len() != 0) {
				t.Fatal("unchanged page was sent again", response.Code)
			}
		})
	}
	// A page too large for the cache has nothing deflated ahead and still decodes.
	app.fragments = newFragmentCache(1)
	_, small := get(paths["room"], "gzip")
	app.fragments = newFragmentCache(32 << 20)
	if _, plain := get(paths["room"], "identity"); small != plain || !bytes.Contains([]byte(plain), []byte("spliced message 39")) {
		t.Fatal("uncached gzipped room differs from identity page")
	}
}
