package web

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/front"
)

func compositionRequest(app *Server, cookie *http.Cookie, route, host, encoding, etag string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+host+route, nil)
	r.AddCookie(cookie)
	r.Header.Set("Accept-Encoding", encoding)
	r.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	front.Deflate(app).ServeHTTP(w, r)
	return w
}

func compositionBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Header().Get("Content-Encoding") != "gzip" {
		return w.Body.String()
	}
	r, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMessagesCapturedGenerationComposition(t *testing.T) {
	for _, budget := range []string{"0", "16"} {
		for _, pieces := range []string{"off", "on"} {
			for _, mutation := range []string{"author", "delete"} {
				for _, conditional := range []bool{false, true} {
					t.Run(fmt.Sprintf("cache%s/pieces%s/%s/conditional%t", budget, pieces, mutation, conditional), func(t *testing.T) {
						t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", budget)
						t.Setenv("CAMPFIRE_RECORDED_PIECES", pieces)
						app, _, cookie, user := testApp(t)
						ctx := context.Background()
						room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Open", "Composition", nil)
						if err != nil {
							t.Fatal(err)
						}
						m, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "composition-client", "<p>compositionbody</p>", "compositionbody")
						if err != nil {
							t.Fatal(err)
						}
						route := fmt.Sprintf("/messages?room_id=%d", room.ID)
						warm := compositionRequest(app, cookie, route, "a.example", "identity", "")
						if warm.Code != 200 || !strings.Contains(warm.Body.String(), "compositionbody") {
							t.Fatalf("warm: %d %s", warm.Code, warm.Body.String())
						}
						foreign, err := sql.Open("sqlite3", "file:"+testDBPath(t, app.DB)+"?_busy_timeout=5000")
						if err != nil {
							t.Fatal(err)
						}
						defer foreign.Close()
						captured, resume := make(chan struct{}), make(chan struct{})
						app.afterGenerationCapture = func() { close(captured); <-resume }
						done := make(chan *httptest.ResponseRecorder, 1)
						etag := ""
						if conditional {
							etag = warm.Header().Get("ETag")
							if etag == "" {
								t.Fatal("missing validator")
							}
						}
						go func() { done <- compositionRequest(app, cookie, route, "a.example", "identity", etag) }()
						<-captured
						if mutation == "author" {
							_, err = foreign.Exec("UPDATE users SET name='CompositionRenamed' WHERE id=?", user.ID)
						} else {
							_, err = foreign.Exec("DELETE FROM messages WHERE id=?", m.ID)
						}
						close(resume)
						got := <-done
						app.afterGenerationCapture = nil
						if err != nil {
							t.Fatal(err)
						}
						wantStatus := http.StatusOK
						if mutation == "delete" {
							wantStatus = http.StatusNoContent
						}
						if got.Code != wantStatus {
							t.Fatalf("stale validator/status: %d, want %d", got.Code, wantStatus)
						}
						if mutation == "author" && !strings.Contains(got.Body.String(), "CompositionRenamed") {
							t.Fatal("stale author bytes")
						}
						if mutation == "delete" && strings.Contains(got.Body.String(), "compositionbody") {
							t.Fatal("deleted message served")
						}
					})
				}
			}
		}
	}
}

func TestRecordedListOriginComposition(t *testing.T) {
	for _, budget := range []string{"0", "16"} {
		for _, encoding := range []string{"identity", "gzip"} {
			t.Run(budget+"/"+encoding, func(t *testing.T) {
				t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", budget)
				t.Setenv("CAMPFIRE_RECORDED_PIECES", "on")
				app, _, cookie, user := testApp(t)
				ctx := context.Background()
				room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Open", "Origin", nil)
				if err != nil {
					t.Fatal(err)
				}
				body := `<action-text-attachment content-type="application/vnd.actiontext.opengraph-embed" href="https://a.example/story" filename="Story"></action-text-attachment>`
				_, err = app.DB.CreateMessage(ctx, user.ID, room.ID, "origin-client", body, "Story")
				if err != nil {
					t.Fatal(err)
				}
				route := fmt.Sprintf("/messages?room_id=%d", room.ID)
				a := compositionRequest(app, cookie, route, "a.example", encoding, "")
				b := compositionRequest(app, cookie, route, "b.example", encoding, "")
				if a.Code != 200 || b.Code != 200 {
					t.Fatalf("statuses: %d %d", a.Code, b.Code)
				}
				if encoding == "gzip" && (a.Header().Get("Content-Encoding") != "gzip" || b.Header().Get("Content-Encoding") != "gzip") {
					t.Fatal("gzip case did not exercise gzip representation")
				}
				warm := compositionBody(t, a)
				if !strings.Contains(warm, "http://a.example/") || strings.Contains(warm, `href="https://a.example/story"`) {
					t.Fatal("host A did not render its permalink and host-sensitive rich text")
				}
				got := compositionBody(t, b)
				// Force an uncached render without any database write.
				app.recordedPieces = false
				app.fragments = newFragmentCache(0)
				app.responses = newResponseCache(0)
				fresh := compositionRequest(app, cookie, route, "b.example", encoding, "")
				if got != compositionBody(t, fresh) {
					t.Error("host B differs from uncached render")
				}
				if !strings.Contains(got, "http://b.example/") || !strings.Contains(got, `href="https://a.example/story"`) {
					t.Fatalf("host B reused host A bytes: %s", got)
				}
			})
		}
	}
}
