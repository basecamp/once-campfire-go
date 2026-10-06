package web

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

// checkRecognizeParity asserts that the compiled recognizer and the legacy
// regex recognizer agree exactly on one (method, path) input: the same
// contract (by table identity), the same params map, and the same error.
func checkRecognizeParity(t *testing.T, method, path string) {
	t.Helper()
	want, wp, werr := legacyRecognize(method, path)
	got, gp, gerr := compiledRecognize(method, path)
	if (werr == nil) != (gerr == nil) {
		t.Fatalf("%s %q: error parity %v != %v", method, path, werr, gerr)
	}
	if werr != nil {
		if werr.Error() != gerr.Error() {
			t.Fatalf("%s %q: error %q != %q", method, path, werr, gerr)
		}
		return
	}
	if (want == nil) != (got == nil) {
		t.Fatalf("%s %q: match parity %v != %v", method, path, want != nil, got != nil)
	}
	if want != nil && want != got {
		t.Fatalf("%s %q: contract %q != %q", method, path, want.Endpoint, got.Endpoint)
	}
	if !reflect.DeepEqual(wp, gp) {
		t.Fatalf("%s %q: params %v != %v", method, path, wp, gp)
	}
}

func allMethods() []string {
	return []string{"GET", "HEAD", "POST", "PATCH", "PUT", "DELETE", "OPTIONS", "TRACE", "FOO", ""}
}

// TestCompiledTableComplete pins that every contract pattern compiles into
// the fast matcher; if a future pattern shape is not supported, this fails
// and the compiler must be extended (the matcher itself would then fall
// back to legacy automatically, but a differential-covered compiler is the
// point of this task).
func TestCompiledTableComplete(t *testing.T) {
	compiledOnce.Do(buildCompiled)
	if !compiledReady.Load() {
		t.Fatal("compiled table unavailable; see test log for the rejected pattern")
	}
	want := 0
	for _, c := range contracts {
		if c.Method == "HEAD" {
			continue
		}
		want++
	}
	got := 0
	for _, groups := range compiledTable {
		for _, routes := range groups {
			got += len(routes)
		}
	}
	if got != want {
		t.Fatalf("compiled %d routes, want %d contract routes", got, want)
	}
}

// TestCompiledRoutesReferenceVectors runs the official recognition vector
// through both recognizers.
func TestCompiledRoutesReferenceVectors(t *testing.T) {
	raw, err := os.ReadFile("../../reference/vectors/campfire_routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Recognitions []struct {
			Verb, Path string
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, c := range vector.Recognitions {
		checkRecognizeParity(t, c.Verb, c.Path)
	}
}

// TestCompiledRoutesGenerated runs a deterministic corpus built from every
// contract pattern: each named route with plain, encoded, dotted, empty and
// weird param values, star tails, format suffixes of every shape, and
// slashed/dotted variants — both matching and non-matching.
func TestCompiledRoutesGenerated(t *testing.T) {
	paramSamples := []string{
		"1", "abc", "42", "a%2Fb", "x.y", "x.y.z", "..", "%.", "%", "%2", "%zz",
		"%c3%a9", "-", "_", "1.2.3", "1.", ".5", "a?b", "%3F", "%2E", "ab%20cd",
		"botkey", "user-1", "users/1", "é", "ünïcode", "a%0Ab",
	}
	starSamples := []string{
		"f", "f.txt", "a/b/c.txt", "a.b", ".lead", "trail.", "a.b.c", "a/b.c/d.txt",
		".", "..", "a%2Fb", "x.y.z", "f.", "a.b.c.d", "%2F", "p?q", "a/b/c",
	}
	formatSamples := []string{"", ".json", ".html", ".turbo_stream", ".5", ".", "..", ".json.x"}
	extraFull := []string{"", "/", "//", "/rooms/1/", "//rooms//1//", "/a%2Fb",
		"/rooms/1%2Ejson", "/rooms/%2E.json", "/rooms/1.json/", "/rooms/1..json"}

	firstParamIndex := func(pat string) int {
		for k := 0; k < len(pat); k++ {
			if pat[k] == ':' || pat[k] == '*' {
				return k
			}
		}
		return -1
	}
	verbs := allMethods()
	for i, c := range contracts {
		body, hasFmt := c.Pattern, false
		if strings.HasSuffix(c.Pattern, "(.:format)") {
			body, hasFmt = strings.TrimSuffix(c.Pattern, "(.:format)"), true
		}
		var built []string
		// Replace the first :param/*star with a rotating sample and recurse;
		// past depth two the remaining params take fixed values, bounding the
		// corpus per contract.
		var walk func(pat string, depth, pi int)
		walk = func(pat string, depth, pi int) {
			k := firstParamIndex(pat)
			if k < 0 {
				if hasFmt {
					for _, f := range formatSamples {
						built = append(built, pat+f)
						if f == "" {
							built = append(built, pat+"/", "//"+pat+"//")
						}
					}
				} else {
					built = append(built, pat)
				}
				return
			}
			j := k + 1
			for j < len(pat) && isIdentByte(pat[j]) {
				j++
			}
			pool, fixed := paramSamples, "1"
			if pat[k] == '*' {
				pool, fixed = starSamples, "f.txt"
			}
			if depth >= 2 {
				walk(pat[:k]+fixed+pat[j:], depth+1, pi+1)
				return
			}
			for _, o := range []int{pi, pi + 1, pi + 5, pi + 11} {
				walk(pat[:k]+pool[o%len(pool)]+pat[j:], depth+1, pi+1)
			}
		}
		walk(body, 0, i%10)
		for _, v := range built {
			checkRecognizeParity(t, verbs[i%len(verbs)], v)
		}
		for _, v := range extraFull {
			checkRecognizeParity(t, c.Method, v)
		}
	}
}

// TestCompiledRoutesEdges runs a hand-written corpus of hostile paths:
// encodings, dots, slashes, unicode, long paths, control bytes, and the
// mailbox/turbo-native shapes, across every method.
func TestCompiledRoutesEdges(t *testing.T) {
	paths := []string{
		"", "/", "//", "///", "/.", "/..", "/...", "/a", "a", "/a/", "/a//b",
		"/a%2Fb/c", "/a%2fb/c", "/café/naïve", "/caf%C3%A9", "/%c3%a9", "/%C3%A9",
		"/rooms", "/rooms/", "/rooms/1", "/rooms/1.", "/rooms/1/", "/rooms//1//",
		"/rooms/1.json", "/rooms/1.turbo_stream", "/rooms/1.2.3", "/rooms/..",
		"/rooms/%2E%2E", "/rooms/%2ejson", "/rooms/.json", "/rooms/1.json/",
		"/rooms/1.json.x", "/rooms/opens", "/rooms/opens/new", "/rooms/opens/5",
		"/rooms/closeds/5", "/rooms/directs/5/edit", "/rooms/1/messages",
		"/rooms/1/messages/2", "/rooms/1/messages/2/turbo_stream", "/rooms/1/messages/2/edit",
		"/rooms/1/messages/2/boosts", "/rooms/1/botkey/messages", "/rooms/1/botkey/messages.json",
		"/rooms/1/botkey/messages/2/boosts/3", "/rooms/1/botkey/messages/2/boosts/3.json",
		"/rooms/1/@42", "/rooms/1/@42.json", "/rooms/1/@4.2", "/rooms/1/@",
		"/users/1", "/users/1/sidebar", "/users/1/avatar", "/users/1/profile/new",
		"/users/1/push_subscriptions/2/test_notifications", "/join/abc", "/join/abc.json",
		"/qr_code/1", "/session/transfers/5", "/session/new", "/first_run/new",
		"/first_run", "/first_run.json", "/up", "/up.json", "/webmanifest",
		"/service-worker", "/searches", "/searches/clear", "/unfurl_link",
		"/account/bots/3/key", "/account/bots/3/key.json", "/account/custom_styles/edit",
		"/autocompletable/users", "/messages/9", "/messages/9/boosts/1",
		"/rails/action_mailbox/postmark/inbound_emails", "/rails/action_mailbox/postmark/inbound_emails.json",
		"/rails/action_mailbox/mandrill/inbound_emails", "/rails/action_mailbox/mailgun/inbound_emails/mime",
		"/rails/conductor/action_mailbox/inbound_emails", "/rails/conductor/action_mailbox/inbound_emails/12",
		"/rails/conductor/action_mailbox/inbound_emails/12/reroute",
		"/rails/conductor/action_mailbox/inbound_emails/sources/new",
		"/rails/active_storage/blobs/redirect/sid/filename.txt",
		"/rails/active_storage/blobs/redirect/sid/a/b/filename.txt",
		"/rails/active_storage/blobs/redirect/sid/.hidden",
		"/rails/active_storage/blobs/redirect/sid/",
		"/rails/active_storage/blobs/redirect/sid/f党组",
		"/rails/active_storage/blobs/proxy/sid/file",
		"/rails/active_storage/blobs/sid/file", "/rails/active_storage/blobs/sid/file.txt",
		"/rails/active_storage/representations/redirect/sid/key/file",
		"/rails/active_storage/representations/proxy/sid/key/file.txt",
		"/rails/active_storage/disk/ek/file", "/rails/active_storage/disk/ek",
		"/rails/active_storage/direct_uploads",
		"/recede_historical_location", "/recede_historical_location.json",
		"/resume_historical_location", "/refresh_historical_location",
		"/rooms/" + strings.Repeat("x", 5000), "/rooms/" + strings.Repeat("a/", 200) + "1",
		"/" + strings.Repeat("z", 10000), "/%ff", "/%FF", "/%", "/%2", "/%zz",
		"/a%b", "/a%2", "/%2f%2f", "/%%2f", "/a\tb", "/a\x7fb", "/a\x00b",
		"/a\rb", "/a\nb", "/a\n", "/.hidden", "/..hidden", "/a..b", "/a...b",
		"/a?b", "/a?b/c", "/rooms/1?x", "/rooms/?x/y", "/%3F", "/%3f", "/%252f",
		"/users/%2E%2E/avatar", "/users/1/avatar%2F..", "/rooms/1/messages%2F2",
		"/rooms/1/messages/2.jpg", "/rooms/1/messages/2.j.p", "/rooms/1/messages/2.",
		"/rooms/1/messages/2/3", "/rooms/1/messages/2/3/4/5/6",
	}
	for _, p := range paths {
		for _, m := range allMethods() {
			checkRecognizeParity(t, m, p)
		}
	}
}

// TestCompiledRoutesFuzzSeeds runs a deterministic randomized corpus through
// both recognizers. The generator (math/rand with fixed seeds) is stable
// across Go releases, so the seeds keep pinning the same inputs.
func TestCompiledRoutesFuzzSeeds(t *testing.T) {
	seeds := []int64{1, 42, 20261006, 7, 987654321, 31337, 0, 999999}
	verbs := allMethods()
	charset := []rune("/.?%+-_@0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	charset = append(charset, 'é', 'ß', '中', '\n', '\r', '\t', ' ', '!', '#', '~')
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		for iter := 0; iter < 3000; iter++ {
			var sb strings.Builder
			n := rng.Intn(90)
			if iter%50 == 0 {
				n = 200 + rng.Intn(1500)
			}
			for i := 0; i < n; i++ {
				switch rng.Intn(8) {
				case 0: // an escape sequence, occasionally malformed
					sb.WriteString("%")
					if rng.Intn(4) > 0 {
						sb.WriteRune(charset[rng.Intn(17)]) // hex-ish
						if rng.Intn(4) > 0 {
							sb.WriteRune(charset[rng.Intn(17)])
						}
					}
				case 1:
					sb.WriteRune(charset[rng.Intn(11)+20]) // letters/digits
				default:
					sb.WriteRune(charset[rng.Intn(len(charset))])
				}
			}
			path := sb.String()
			checkRecognizeParity(t, verbs[rng.Intn(len(verbs))], path)
		}
	}
}

// TestCompiledRoutesFlagParsing pins the CAMPFIRE_COMPILED_ROUTES contract:
// same shapes as the other CAMPFIRE_* switches, unknown values keep the
// default on.
func TestCompiledRoutesFlagParsing(t *testing.T) {
	cases := []struct {
		raw     string
		enabled bool
		valid   bool
	}{
		{"", true, true}, {"on", true, true}, {"true", true, true}, {"1", true, true},
		{"ON", true, true}, {" True ", true, true}, {"off", false, true}, {"false", false, true},
		{"0", false, true}, {"OFF", false, true}, {"sometimes", true, false},
	}
	for _, c := range cases {
		enabled, valid := parseCompiledRoutes(c.raw)
		if valid != c.valid || enabled != c.enabled {
			t.Errorf("CAMPFIRE_COMPILED_ROUTES=%q: got %v/%v, want %v/%v", c.raw, enabled, valid, c.enabled, c.valid)
		}
	}
}

// TestCompiledRoutesFlagOff pins that recognize with the flag off is exactly
// the legacy recognizer (dispatch correctness), and that the flag can be
// flipped between calls.
func TestCompiledRoutesFlagOff(t *testing.T) {
	check := func() {
		for _, p := range []string{"/rooms/1", "/rooms/1.json", "/users/9/sidebar",
			"/rails/active_storage/blobs/sid/f.txt", "/no/match/at/all", "/", "//",
			"/a\nb", "/%ff"} {
			method := "GET"
			want, wp, werr := legacyRecognize(method, p)
			got, gp, gerr := recognize(method, p)
			if (werr == nil) != (gerr == nil) || (want == nil) != (got == nil) {
				t.Fatalf("flag off %q: %v/%v vs %v/%v", p, want, werr, got, gerr)
			}
			if !reflect.DeepEqual(wp, gp) {
				t.Fatalf("flag off %q: params %v != %v", p, wp, gp)
			}
		}
	}
	t.Setenv("CAMPFIRE_COMPILED_ROUTES", "off")
	resetCompiledRoutesFlagForTest()
	check()
	t.Setenv("CAMPFIRE_COMPILED_ROUTES", "on")
	resetCompiledRoutesFlagForTest()
	check()
}

// TestCompiledRoutesRouteHTTP runs the full routeHTTP flow — status codes,
// path-value population, and the format/path rewrite — under both the
// compiled and legacy recognizers, on mailbox, turbo-native and delegated
// routes.
func TestCompiledRoutesRouteHTTP(t *testing.T) {
	s := &Server{mux: &router{}}
	var gotController, gotAction, gotID, gotRoomID, gotFormat string
	s.mux.HandleFunc("GET /rooms/{id}", func(w http.ResponseWriter, r *http.Request) {
		gotController, gotAction = r.PathValue("controller"), r.PathValue("action")
		gotRoomID, gotID = r.PathValue("room_id"), r.PathValue("id")
		gotFormat = r.PathValue("format")
		w.WriteHeader(200)
	})
	s.mux.HandleFunc("GET /messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	requests := []struct {
		method, path string
		status       int
		body         string
		ct           string
	}{
		{"GET", "/rooms/1.json", 200, "", ""},
		{"GET", "/rooms/1", 200, "", ""},
		{"GET", "/rooms/1.turbo_stream", 200, "", ""},
		{"GET", "/no/such/route", 404, "", ""},
		{"PATCH", "/rooms/1", 404, "", ""},
		{"GET", "/rails/conductor/action_mailbox/inbound_emails", 403, "", ""},
		{"POST", "/rails/action_mailbox/postmark/inbound_emails", 404, "", ""},
		{"GET", "/rails/action_mailbox/mandrill/inbound_emails", 404, "", ""},
		{"GET", "/recede_historical_location", 200, "Going back…", "text/html; charset=utf-8"},
		{"GET", "/resume_historical_location", 200, "Staying put…", "text/html; charset=utf-8"},
		{"GET", "/refresh_historical_location", 200, "Refreshing…", "text/html; charset=utf-8"},
		{"GET", "/messages/42", 200, "", ""},
	}
	run := func() {
		for _, rq := range requests {
			req := httptest.NewRequest(rq.method, "https://example.test"+rq.path, nil)
			rec := httptest.NewRecorder()
			s.routeHTTP(rec, req)
			if rec.Code != rq.status {
				t.Errorf("%s %s: status %d != %d", rq.method, rq.path, rec.Code, rq.status)
			}
			if rq.body != "" && rec.Body.String() != rq.body {
				t.Errorf("%s %s: body %q != %q", rq.method, rq.path, rec.Body.String(), rq.body)
			}
			if rq.ct != "" && rec.Header().Get("Content-Type") != rq.ct {
				t.Errorf("%s %s: content-type %q != %q", rq.method, rq.path, rec.Header().Get("Content-Type"), rq.ct)
			}
			if rq.path == "/rooms/1.json" {
				if gotController != "rooms" || gotAction != "show" || gotID != "1" || gotRoomID != "" {
					t.Errorf("rooms/1.json: controller/action/id %q/%q/%q", gotController, gotAction, gotID)
				}
				if gotFormat != "json" {
					t.Errorf("rooms/1.json: format %q != json", gotFormat)
				}
				if req.URL.Path != "/rooms/1" || req.URL.RawPath != "/rooms/1" {
					t.Errorf("rooms/1.json: rewritten URL %q %q", req.URL.Path, req.URL.RawPath)
				}
			}
		}
	}
	t.Setenv("CAMPFIRE_COMPILED_ROUTES", "off")
	resetCompiledRoutesFlagForTest()
	run()
	t.Setenv("CAMPFIRE_COMPILED_ROUTES", "on")
	resetCompiledRoutesFlagForTest()
	run()
}

// TestCompiledMuxDifferential registers the production mux route set (the
// static registrations plus the dynamic rooms/opens|closeds|directs and PWA
// variants) with recording handlers and asserts the compiled and regex mux
// paths agree on status, matched route and path values for a corpus of
// requests, under both CAMPFIRE_COMPILED_ROUTES states.
func TestCompiledMuxDifferential(t *testing.T) {
	patterns := []string{
		"POST /unfurl_link", "GET /qr_code/{code}", "GET /autocompletable/users",
		"GET /autocompletable/users.json", "GET /cable", "GET /up", "GET /up.json",
		"GET /session/new", "POST /session", "DELETE /session", "GET /first_run",
		"POST /first_run", "GET /{$}", "GET /rooms", "GET /rooms/{id}",
		"GET /rooms/{id}/messages", "POST /rooms/{id}/messages",
		"GET /users/{user}/sidebar", "GET /users/sidebar", "GET /searches",
		"POST /searches", "DELETE /searches/clear", "GET /account/edit",
		"PATCH /account", "PUT /account", "POST /account/join_code",
		"GET /account/custom_styles/edit", "PATCH /account/custom_styles",
		"PUT /account/custom_styles", "GET /users/{user}/profile",
		"PATCH /users/{user}/profile", "PUT /users/{user}/profile",
		"GET /users/{user}", "POST /users/{user}/ban", "DELETE /users/{user}/ban",
		"GET /account/users", "PATCH /account/users/{user}",
		"PUT /account/users/{user}", "DELETE /account/users/{user}",
		"GET /join/{code}", "POST /join/{code}", "GET /account/bots",
		"GET /account/bots/new", "GET /account/bots/{bot}/edit", "POST /account/bots",
		"PATCH /account/bots/{bot}", "PUT /account/bots/{bot}",
		"DELETE /account/bots/{bot}", "PATCH /account/bots/{bot}/key",
		"PUT /account/bots/{bot}/key", "GET /session/transfers/{token}",
		"PATCH /session/transfers/{token}", "PUT /session/transfers/{token}",
		"GET /rails/active_storage/representations/redirect/{token}/{variation}/{filename...}",
		"GET /rails/active_storage/representations/proxy/{token}/{variation}/{filename...}",
		"GET /rails/active_storage/representations/{token}/{variation}/{filename...}",
		"POST /rails/active_storage/direct_uploads", "PUT /rails/active_storage/disk/{token}",
		"GET /rails/active_storage/disk/{token}/{filename...}",
		"GET /rails/active_storage/blobs/redirect/{token}/{filename...}",
		"GET /rails/active_storage/blobs/proxy/{token}/{filename...}",
		"GET /rails/active_storage/blobs/{token}/{filename...}", "DELETE /rooms/{id}",
		"GET /rooms/{id}/involvement", "PATCH /rooms/{id}/involvement",
		"PUT /rooms/{id}/involvement", "GET /rooms/{id}/{anchor}",
		"GET /users/{token}/avatar", "DELETE /users/{user}/avatar",
		"GET /account/logo", "DELETE /account/logo", "GET /messages", "POST /messages",
		"GET /messages/{message}", "GET /messages/{message}/edit",
		"PATCH /messages/{message}", "PUT /messages/{message}",
		"DELETE /messages/{message}", "GET /rooms/{id}/messages/{message}",
		"GET /rooms/{id}/messages/{message}/edit", "PATCH /rooms/{id}/messages/{message}",
		"PUT /rooms/{id}/messages/{message}", "DELETE /rooms/{id}/messages/{message}",
		"GET /messages/{message}/boosts", "GET /messages/{message}/boosts/new",
		"POST /messages/{message}/boosts", "DELETE /messages/{message}/boosts/{boost}",
		"GET /rooms/{id}/refresh", "GET /users/{user}/push_subscriptions",
		"POST /users/{user}/push_subscriptions",
		"DELETE /users/{user}/push_subscriptions/{subscription}",
		// dynamic registrations: rooms opens/closeds/directs prefixes and PWA
		"GET /rooms/opens/new", "POST /rooms/opens", "GET /rooms/opens/{id}/edit",
		"GET /rooms/opens/{id}", "PATCH /rooms/opens/{id}", "PUT /rooms/opens/{id}",
		"DELETE /rooms/opens/{id}", "GET /rooms/closeds/new", "POST /rooms/closeds",
		"GET /rooms/closeds/{id}/edit", "GET /rooms/closeds/{id}",
		"PATCH /rooms/closeds/{id}", "PUT /rooms/closeds/{id}",
		"DELETE /rooms/closeds/{id}", "GET /rooms/directs/new", "POST /rooms/directs",
		"GET /rooms/directs/{id}/edit", "GET /rooms/directs/{id}",
		"PATCH /rooms/directs/{id}", "PUT /rooms/directs/{id}",
		"DELETE /rooms/directs/{id}", "GET /webmanifest", "GET /webmanifest.json",
		"GET /service-worker", "GET /service-worker.js",
		"POST /users/{user}/push_subscriptions/{subscription}/test_notifications",
	}
	var namesOf = func(pattern string) []string {
		var names []string
		for _, part := range strings.Split(strings.SplitN(pattern, " ", 2)[1], "/") {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				names = append(names, strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}"))
			}
		}
		return names
	}
	type hit struct {
		index  int
		values map[string]string
	}
	rt := &router{}
	hits := make([]hit, len(patterns))
	for i, pattern := range patterns {
		names := namesOf(pattern)
		handler := func(w http.ResponseWriter, r *http.Request) {
			hits[i] = hit{index: i, values: map[string]string{}}
			for _, n := range names {
				if v := r.PathValue(n); v != "" {
					hits[i].values[n] = v
				}
			}
			w.WriteHeader(200)
		}
		rt.HandleFunc(pattern, handler)
	}
	var run = func(t *testing.T) {
		t.Helper()
		reqs := []string{"/", "/rooms", "/rooms/1", "/rooms/1/", "/rooms/1/messages/9",
			"/rooms/1/messages/9/edit", "/users/sidebar", "/users/7/sidebar",
			"/users/7", "/users/a%2Fb", "/account/bots/3/key", "/account/bots/3/ke",
			"/messages", "/messages/9", "/messages/9/boosts/2", "/messages/9/boosts",
			"/messages/9/boosts/new", "/up", "/up.json", "/up.json.x", "/webmanifest",
			"/webmanifest.json", "/service-worker.js", "/session/new", "/join/abc",
			"/qr_code/5", "/cable", "/autocompletable/users", "/autocompletable/users.json",
			"/autocompletable/users.json5", "/searches/clear", "/unfurl_link", "/first_run",
			"/rails/active_storage/blobs/redirect/tok/f.txt",
			"/rails/active_storage/blobs/redirect/tok/a/b/f.txt",
			"/rails/active_storage/blobs/redirect/tok/",
			"/rails/active_storage/representations/proxy/tok/var/f.txt",
			"/rails/active_storage/disk/tok/1.png", "/rails/active_storage/disk/tok",
			"/rails/active_storage/direct_uploads", "/rooms/opens/5",
			"/rooms/closeds/5/edit", "/rooms/directs", "/rooms/directs/5",
			"/users/1/push_subscriptions/2/test_notifications",
			"/users/1/push_subscriptions", "/no/such/route", "/no/such/route/x",
			"/rooms/1.5", "/rooms/1.5.6", "/%zz", "/a%zz", "/rooms/%zz", "/%",
			"/rooms/1?x", "/rooms/%31", "/caf%C3%A9", "/users/中", "//", "///", "/x//y",
			"/rooms//1", "/rooms/..", "/users/..", "/a/../../b", "/users/a.b.c",
			"/messages/9/boosts/2/3", "/rooms/1/messages", "/rooms/1/refresh",
			"/a\nb", "/rooms/1\nx",
		}
		for _, p := range reqs {
			for _, m := range []string{"GET", "HEAD", "POST", "PATCH", "PUT", "DELETE", "OPTIONS", "TRACE"} {
				u, err := url.Parse("https://example.test" + p)
				if err != nil {
					if p == "/a\nb" { // constructed manually below
						u = &url.URL{Scheme: "https", Host: "example.test", Path: p, RawPath: p}
					} else {
						continue // never reaches a router: the server rejects the request line
					}
				}
				mk := func() *http.Request { return &http.Request{Method: m, URL: u, Header: make(http.Header)} }
				hits = make([]hit, len(patterns))
				rec := httptest.NewRecorder()
				rt.ServeHTTP(rec, mk()) // compiled path under the flag above
				wantStatus, wantIndex, wantValues := rec.Code, -1, map[string]string{}
				for i := range hits {
					if hits[i].index == i {
						wantIndex, wantValues = i, hits[i].values
					}
				}
				hits = make([]hit, len(patterns))
				rec2 := httptest.NewRecorder()
				rt.serveLegacy(rec2, mk()) // the regex recognizer, always
				if rec2.Code != wantStatus {
					t.Fatalf("%s %s: status %d != %d", m, p, rec2.Code, wantStatus)
				}
				found := -1
				for i := range hits {
					if hits[i].index == i {
						found = i
					}
				}
				if found != wantIndex {
					t.Fatalf("%s %s: route %d != %d", m, p, found, wantIndex)
				}
				if wantIndex >= 0 && !reflect.DeepEqual(hits[wantIndex].values, wantValues) {
					t.Fatalf("%s %s: values %v != %v", m, p, hits[wantIndex].values, wantValues)
				}
			}
		}
	}
	t.Setenv("CAMPFIRE_COMPILED_ROUTES", "on")
	resetCompiledRoutesFlagForTest()
	run(t)
	t.Setenv("CAMPFIRE_COMPILED_ROUTES", "off")
	resetCompiledRoutesFlagForTest()
	run(t)
}

func benchPaths() []struct {
	method, path string
} {
	return []struct{ method, path string }{
		{"GET", "/rooms/1/messages/500"},
		{"GET", "/rooms"},
		{"GET", "/users/ab%2Fcd/sidebar"},
		{"GET", "/rails/active_storage/blobs/redirect/abc/def.txt"},
		{"POST", "/rooms/1/botkey/messages/2/boosts"},
		{"GET", "/no/such/route"},
		{"GET", "/"},
		{"GET", "/first_run"},
	}
}

func BenchmarkRecognizeLegacy(b *testing.B) {
	for _, c := range benchPaths() {
		b.Run(c.method+" "+c.path, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				legacyRecognize(c.method, c.path)
			}
		})
	}
}

func BenchmarkRecognizeCompiled(b *testing.B) {
	for _, c := range benchPaths() {
		b.Run(c.method+" "+c.path, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				compiledRecognize(c.method, c.path)
			}
		})
	}
}

func benchMux(b *testing.B, compiled bool) {
	rt := &router{}
	h := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	for _, p := range []string{
		"GET /rooms/{id}", "GET /rooms/{id}/messages/{message}",
		"GET /users/{user}/sidebar", "GET /users/sidebar", "GET /messages/{message}",
		"GET /rails/active_storage/blobs/redirect/{token}/{filename...}",
		"GET /rails/active_storage/blobs/proxy/{token}/{filename...}",
		"GET /rails/active_storage/representations/redirect/{token}/{variation}/{filename...}",
		"POST /rooms/{id}/messages", "PATCH /account", "GET /{$}", "GET /up",
		"GET /up.json", "GET /account/bots/{bot}/edit", "GET /session/transfers/{token}",
		"DELETE /messages/{message}/boosts/{boost}", "GET /search", "GET /users/{user}",
	} {
		rt.HandleFunc(p, h)
	}
	paths := []string{"/rooms/1/messages/9", "/", "/up.json", "/users/sidebar",
		"/users/9/sidebar", "/rails/active_storage/blobs/redirect/abc/def.txt",
		"/rails/active_storage/representations/redirect/a/b/c.txt", "/nope"}
	if !compiled {
		b.Setenv("CAMPFIRE_COMPILED_ROUTES", "off")
	} else {
		b.Setenv("CAMPFIRE_COMPILED_ROUTES", "on")
	}
	resetCompiledRoutesFlagForTest()
	for _, p := range paths {
		u := &url.URL{Scheme: "https", Host: "x.test", Path: p}
		b.Run("GET "+p, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				r := &http.Request{Method: "GET", URL: u, Header: make(http.Header)}
				rt.ServeHTTP(rec, r)
			}
		})
	}
}

func BenchmarkMuxLegacy(b *testing.B)   { benchMux(b, false) }
func BenchmarkMuxCompiled(b *testing.B) { benchMux(b, true) }
