package engine

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/engine/difftest"
	"github.com/basecamp/once-campfire-go/internal/front"
)

// legacyFixture is a deterministic stand-in for web.Server: fixed status,
// headers and bytes per method and path, with no clock or randomness, so two
// exchanges of one request must be byte-identical.
func legacyFixture() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			io.WriteString(w, "<html>home</html>")
		case r.Method == "GET" && r.URL.Path == "/rooms/1":
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("ETag", `"room-1"`)
			io.WriteString(w, "room one fixed bytes")
		case r.Method == "HEAD" && r.URL.Path == "/rooms/1":
			w.Header().Set("ETag", `"room-1"`)
			io.WriteString(w, "room one fixed bytes")
		case r.Method == "GET" && r.URL.Path == "/rooms/1/messages":
			body := "messages page"
			if before := r.URL.Query().Get("before"); before != "" {
				body = "messages before " + before
			}
			io.WriteString(w, body)
		case r.Method == "GET" && r.URL.Path == "/users/sidebar":
			w.Header().Set("Content-Type", "text/vnd.turbo-stream.html; charset=utf-8")
			io.WriteString(w, `<turbo-stream action="append"></turbo-stream>`)
		case r.Method == "POST" && r.URL.Path == "/rooms/1/messages":
			payload, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusCreated)
			w.Write(append([]byte("posted:"), payload...))
		default:
			http.NotFound(w, r)
		}
	})
}

// requestCase is one entry of the byte-identity table: page shapes, query
// strings, methods the router distinguishes, request bodies, HEAD and paths
// unknown to the fixture.
type requestCase struct {
	name   string
	method string
	path   string
	header http.Header
	body   []byte
}

var passThroughCases = []requestCase{
	{"home", "GET", "/", nil, nil},
	{"room", "GET", "/rooms/1", nil, nil},
	{"room accepts gzip", "GET", "/rooms/1", http.Header{"Accept-Encoding": {"gzip"}}, nil},
	{"room accepts identity", "GET", "/rooms/1", http.Header{"Accept-Encoding": {"identity"}}, nil},
	{"messages page", "GET", "/rooms/1/messages", nil, nil},
	{"messages before", "GET", "/rooms/1/messages?before=5", nil, nil},
	{"sidebar", "GET", "/users/sidebar", nil, nil},
	{"head room", "HEAD", "/rooms/1", nil, nil},
	{"post message", "POST", "/rooms/1/messages", http.Header{"Content-Type": {"text/plain"}}, []byte("hello world")},
	{"unknown path", "GET", "/nope", nil, nil},
	{"unknown method", "DELETE", "/rooms/1", nil, nil},
}

// TestPassThroughByteIdentical pins the strangler contract: with no owned
// routes the engine serves status, headers and body byte-identical to the
// legacy handler for a table of representative requests. The comparison runs
// through difftest, the same harness later differential tests use.
func TestPassThroughByteIdentical(t *testing.T) {
	legacy := legacyFixture()
	for _, mode := range []Mode{ModeOn, ModeForce, ModeOff} {
		t.Run(mode.String(), func(t *testing.T) {
			engine := New(legacy, Config{Mode: mode})
			pair := difftest.New(engine, legacy)
			defer pair.Close()

			for _, tc := range passThroughCases {
				t.Run(tc.name, func(t *testing.T) {
					got, want, err := pair.Exchange(difftest.Request{
						Method: tc.method,
						Path:   tc.path,
						Header: tc.header,
						Body:   tc.body,
					})
					if err != nil {
						t.Fatal(err)
					}
					if got.Status != want.Status {
						t.Fatalf("status = %d, legacy = %d", got.Status, want.Status)
					}
					if !reflect.DeepEqual(got.Header, want.Header) {
						t.Fatalf("headers differ\n engine: %v\n legacy: %v", got.Header, want.Header)
					}
					if !bytes.Equal(got.Body, want.Body) {
						t.Fatalf("body differs\n engine: %q\n legacy: %q", got.Body, want.Body)
					}
				})
			}
			if got := engine.Fallbacks(); got != int64(len(passThroughCases)) {
				t.Fatalf("fallbacks = %d, want %d", got, len(passThroughCases))
			}
		})
	}
}

// TestUnknownPath404Identical pins that a path unknown to both results in the
// same 404 status and body.
func TestUnknownPath404Identical(t *testing.T) {
	legacy := legacyFixture()
	engine := New(legacy, Config{Mode: ModeOn})

	got, want, err := difftest.Run(engine, legacy, difftest.Request{Method: "GET", Path: "/definitely-unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != http.StatusNotFound {
		t.Fatalf("engine status = %d, want 404", got.Status)
	}
	if got.Status != want.Status {
		t.Fatalf("engine status = %d, legacy = %d", got.Status, want.Status)
	}
	if !bytes.Equal(got.Body, want.Body) {
		t.Fatalf("engine body = %q, legacy = %q", got.Body, want.Body)
	}
}

func probeHandlers() (legacy, probe http.Handler) {
	legacy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Source", "legacy")
		io.WriteString(w, "legacy")
	})
	probe = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Source", "engine")
		io.WriteString(w, "probe")
	})
	return legacy, probe
}

// TestOwnershipModes pins the mode contract: off disables ownership for routes
// the engine would own, on and force serve them; owned routes leave the
// fallback counter at zero, everything delegated increments it.
func TestOwnershipModes(t *testing.T) {
	legacy, probe := probeHandlers()
	for _, tc := range []struct {
		mode  Mode
		owned bool
	}{
		{ModeOff, false},
		{ModeOn, true},
		{ModeForce, true},
	} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			engine := New(legacy, Config{Mode: tc.mode})
			engine.handle("GET", "/probe", probe)

			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest("GET", "/probe", nil))
			wantSource := "legacy"
			if tc.owned {
				wantSource = "engine"
			}
			if got := response.Header().Get("X-Source"); got != wantSource {
				t.Fatalf("owned probe served by %q, want %q", got, wantSource)
			}
			if tc.owned {
				if got := engine.Fallbacks(); got != 0 {
					t.Fatalf("fallbacks after owned probe = %d, want 0", got)
				}
			}

			before := engine.Fallbacks()
			unowned := httptest.NewRecorder()
			engine.ServeHTTP(unowned, httptest.NewRequest("GET", "/unowned", nil))
			if got := unowned.Header().Get("X-Source"); got != "legacy" {
				t.Fatalf("unowned path served by %q, want legacy", got)
			}
			if got := engine.Fallbacks(); got != before+1 {
				t.Fatalf("fallbacks after unowned path = %d, want %d", got, before+1)
			}

			// A method the route does not own falls back for that request.
			wrongMethod := httptest.NewRecorder()
			engine.ServeHTTP(wrongMethod, httptest.NewRequest("POST", "/probe", nil))
			if got := wrongMethod.Header().Get("X-Source"); got != "legacy" {
				t.Fatalf("unowned method served by %q, want legacy", got)
			}
			if got := engine.Fallbacks(); got != before+2 {
				t.Fatalf("fallbacks after unowned method = %d, want %d", got, before+2)
			}
		})
	}
}

// TestEscapedPathOwnership pins that ownership keys on r.URL.EscapedPath(),
// the form the legacy router recognizes. An encoded slash is a different
// escaped path from the decoded one, so it must fall back rather than serve
// the entry registered under the decoded path.
func TestEscapedPathOwnership(t *testing.T) {
	legacy, probe := probeHandlers()
	engine := New(legacy, Config{Mode: ModeForce})
	engine.handle("GET", "/rooms/1", probe)

	plain := httptest.NewRecorder()
	engine.ServeHTTP(plain, httptest.NewRequest("GET", "/rooms/1", nil))
	if got := plain.Header().Get("X-Source"); got != "engine" {
		t.Fatalf("plain path served by %q, want engine", got)
	}

	encoded := httptest.NewRecorder()
	engine.ServeHTTP(encoded, httptest.NewRequest("GET", "/rooms%2F1", nil))
	if got := encoded.Header().Get("X-Source"); got != "legacy" {
		t.Fatalf("encoded-slash path served by %q, want legacy", got)
	}
	if got := engine.Fallbacks(); got != 1 {
		t.Fatalf("fallbacks = %d, want 1", got)
	}
}

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  Mode
		ok    bool
	}{
		{"", ModeOn, true},
		{"on", ModeOn, true},
		{"ON", ModeOn, true},
		{"off", ModeOff, true},
		{"OFF", ModeOff, true},
		{" off ", ModeOff, true},
		{"force", ModeForce, true},
		{"Force", ModeForce, true},
		{"bogus", ModeOn, false},
		{"of", ModeOn, false},
	} {
		got, ok := ParseMode(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseMode(%q) = (%v, %t), want (%v, %t)", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func gzipMember(t *testing.T, plain string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// TestFallbackKeepsLegacyGzip pins the encoding composition: the engine wraps
// the legacy handler, and fallback responses are still gzipped by
// front.Deflate exactly as they were before the engine existed, in every mode.
func TestFallbackKeepsLegacyGzip(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<html>legacy body</html>")
	})
	legacy := front.Deflate(app)
	for _, mode := range []Mode{ModeOn, ModeOff} {
		t.Run(mode.String(), func(t *testing.T) {
			engine := New(legacy, Config{Mode: mode})

			gzipRequest := func() *http.Request {
				request := httptest.NewRequest("GET", "/", nil)
				request.Header.Set("Accept-Encoding", "gzip")
				return request
			}
			direct := httptest.NewRecorder()
			legacy.ServeHTTP(direct, gzipRequest())
			through := httptest.NewRecorder()
			engine.ServeHTTP(through, gzipRequest())

			if through.Code != direct.Code {
				t.Fatalf("status = %d, legacy = %d", through.Code, direct.Code)
			}
			if got := through.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if !bytes.Equal(through.Body.Bytes(), direct.Body.Bytes()) {
				t.Fatalf("gzip body changed: %d bytes through engine, %d direct", through.Body.Len(), direct.Body.Len())
			}
		})
	}
}

// TestOwnedRouteOwnsEncoding pins the takeover half of the contract: a route
// the engine serves sends its precomposed encoded bytes untouched, without the
// legacy Deflate wrapper adding a second gzip layer, in both owned modes.
func TestOwnedRouteOwnsEncoding(t *testing.T) {
	member := gzipMember(t, "engine body")
	legacy := front.Deflate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "legacy body")
	}))
	for _, mode := range []Mode{ModeOn, ModeForce} {
		t.Run(mode.String(), func(t *testing.T) {
			probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Vary", "Accept-Encoding")
				w.Write(member)
			})
			engine := New(legacy, Config{Mode: mode})
			engine.handle("GET", "/probe", probe)

			request := httptest.NewRequest("GET", "/probe", nil)
			request.Header.Set("Accept-Encoding", "gzip")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)

			if got := response.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if got := response.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("Vary = %q, want Accept-Encoding", got)
			}
			if !bytes.Equal(response.Body.Bytes(), member) {
				t.Fatalf("owned body re-encoded: %d bytes in, %d out", len(member), response.Body.Len())
			}
		})
	}
}

// nopResponseWriter is the cheapest possible http.ResponseWriter: no status
// tracking, no buffering, no header allocation. It lets AllocsPerRun measure
// the engine seam alone rather than recorder internals.
type nopResponseWriter struct {
	header http.Header
}

func (w *nopResponseWriter) Header() http.Header         { return w.header }
func (w *nopResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nopResponseWriter) WriteHeader(int)             {}

var fixedLegacyBody = []byte("legacy")

// TestFallbackPathZeroAllocations pins the seam contract: a delegated request
// costs one map lookup and one atomic add, and allocates nothing.
func TestFallbackPathZeroAllocations(t *testing.T) {
	legacy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixedLegacyBody)
	})
	engine := New(legacy, Config{Mode: ModeOn})
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	writer := &nopResponseWriter{header: make(http.Header)}

	if allocs := testing.AllocsPerRun(200, func() {
		engine.ServeHTTP(writer, request)
	}); allocs != 0 {
		t.Fatalf("fallback path allocated %.2f objects per run, want 0", allocs)
	}
}
