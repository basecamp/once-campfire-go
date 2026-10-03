package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMessageConditionalGet(t *testing.T) {
	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "fresh", "fresh", "fresh"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d/messages", rooms[0].ID)
	request := func(headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	first := request(nil)
	etag := first.Header().Get("ETag")
	modified := first.Header().Get("Last-Modified")
	if first.Code != 200 || etag == "" || modified == "" {
		t.Fatal(first.Code, first.Header())
	}
	for _, headers := range []map[string]string{{"If-None-Match": etag}, {"If-None-Match": "\"other\", " + etag}, {"If-Modified-Since": modified}} {
		w := request(headers)
		if w.Code != 304 || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request(map[string]string{"If-None-Match": "\"stale\"", "If-Modified-Since": time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)})
	if w.Code != 200 {
		t.Fatal("ETag did not take precedence", w.Code)
	}
	frame := request(map[string]string{"Turbo-Frame": "messages"})
	if frame.Header().Get("ETag") == etag {
		t.Fatal("frame did not change validator")
	}
	if !strings.Contains(first.Body.String(), "fresh") {
		t.Fatal("empty response")
	}
}
