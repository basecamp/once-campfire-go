package web

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// testDBPath returns the on-disk path of the app's main database so a test can
// open a SEPARATE connection and write outside the audited helpers.
func testDBPath(t testing.TB, db *database.DB) string {
	t.Helper()
	var path string
	if err := db.Read.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCapturedGenerationRevocation(t *testing.T) {
	for _, budget := range []string{"0", "16"} {
		for _, route := range []string{"/searches?q=revocationneedlebody", "/users/sidebar"} {
			t.Run(budget+route, func(t *testing.T) {
				t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", budget)
				app, server, cookie, user := testApp(t)
				ctx := context.Background()
				room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "PrivateBoundaryRoom", []int64{user.ID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "boundary-client", "<p>revocationneedlebody</p>", "revocationneedlebody"); err != nil {
					t.Fatal(err)
				}
				marker := "message_boundary-client"
				if route == "/users/sidebar" {
					marker = "PrivateBoundaryRoom"
				}
				response, body := perform(t, server, "GET", route, "", nil, cookie)
				if response.StatusCode != 200 || !strings.Contains(string(body), marker) {
					t.Fatalf("warm %s: %d missing marker", route, response.StatusCode)
				}
				foreign, err := sql.Open("sqlite3", "file:"+testDBPath(t, app.DB)+"?_busy_timeout=5000")
				if err != nil {
					t.Fatal(err)
				}
				defer foreign.Close()
				captured, resume := make(chan struct{}), make(chan struct{})
				app.afterGenerationCapture = func() { close(captured); <-resume }
				req := httptest.NewRequest("GET", route, nil)
				req.AddCookie(cookie)
				rec := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { app.ServeHTTP(rec, req); close(done) }()
				<-captured
				_, err = foreign.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=? AND user_id=?", room.ID, user.ID)
				close(resume)
				<-done
				app.afterGenerationCapture = nil
				if err != nil {
					t.Fatal(err)
				}
				if rec.Code != 200 {
					t.Fatalf("status %d", rec.Code)
				}
				if strings.Contains(rec.Body.String(), marker) {
					t.Fatalf("%s served revoked content from captured generation", route)
				}
			})
		}
	}
}

// TestSearchCacheObservesForeignMembershipRevocation pins the reviewer's
// scenario: after the search result cache is warmed, a membership deleted by
// a SEPARATE SQLite connection (no audited helper, no local version bump) must
// not be served from the cache; the next search excludes the private message.
func TestSearchCacheObservesForeignMembershipRevocation(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	closed, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Private", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, closed.ID, "revocation-client", "<p>revocationneedlebody</p>", "revocationneedlebody"); err != nil {
		t.Fatal(err)
	}
	// The message's DOM id appears only when the fragment renders; the query
	// text itself is echoed on the page, so it cannot be the marker. The
	// index insert is after-commit work: retry until the first search sees it
	// (and thereby warms the result cache).
	deadline := time.Now().Add(3 * time.Second)
	var body []byte
	for {
		response, current := perform(t, server, "GET", "/searches?q=revocationneedlebody", "", nil, cookie)
		if response.StatusCode != 200 {
			t.Fatalf("warm search: status %d", response.StatusCode)
		}
		body = current
		if strings.Contains(string(body), "message_revocation-client") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("warm search never rendered the message")
		}
		time.Sleep(20 * time.Millisecond)
	}
	foreign, err := sql.Open("sqlite3", "file:"+testDBPath(t, app.DB)+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err := foreign.ExecContext(ctx, "DELETE FROM memberships WHERE room_id=? AND user_id=?", closed.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	response, body := perform(t, server, "GET", "/searches?q=revocationneedlebody", "", nil, cookie)
	if response.StatusCode != 200 {
		t.Fatalf("post-revocation search: status %d", response.StatusCode)
	}
	if strings.Contains(string(body), "message_revocation-client") {
		t.Fatal("revoked membership served a private message")
	}
}
