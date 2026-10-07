package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserCompatibilityRunsAfterAuthentication(t *testing.T) {
	app, _, cookie, _ := testApp(t)
	old := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15"
	for _, test := range []struct {
		path                 string
		authenticated, frame bool
		status               int
		blocked              bool
	}{
		{"/users/me/profile", false, false, 302, false},
		{"/users/me/profile", true, false, 200, true},
		{"/users/me/profile", true, true, 200, true},
		{"/webmanifest.json", false, false, 200, true},
		{"/service-worker.js", false, false, 200, true},
		{"/session/new", false, false, 200, true},
		{"/up", false, false, 200, false},
	} {
		t.Run(test.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", test.path, nil)
			r.Header.Set("User-Agent", old)
			r.Header.Set("Accept", "application/json")
			if test.authenticated {
				r.AddCookie(cookie)
			}
			if test.frame {
				r.Header.Set("Turbo-Frame", "content")
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			blocked := strings.Contains(w.Body.String(), "Upgrade to a supported web browser")
			if w.Code != test.status || blocked != test.blocked {
				t.Fatalf("status=%d blocked=%v: %s", w.Code, blocked, w.Body.String())
			}
			if blocked && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Fatal(w.Header())
			}
			if test.frame && strings.Contains(w.Body.String(), "stylesheet") {
				t.Fatal("frame response included the application layout")
			}
		})
	}
	// Alternate platforms on the same server: parsed metadata belongs to the
	// request, not the session, user, or the previous page render.
	for _, mobile := range []bool{true, false, true} {
		r := httptest.NewRequest(http.MethodGet, "/users/me/profile", nil)
		r.AddCookie(cookie)
		if mobile {
			r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1")
		} else {
			r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), "Add to Home Screen") != mobile {
			t.Fatal(w.Code, mobile, w.Body.String())
		}
	}
}
