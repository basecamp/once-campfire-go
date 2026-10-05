package engine

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

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
	header map[string]string
	body   []byte
}

var passThroughCases = []requestCase{
	{"home", "GET", "/", nil, nil},
	{"room", "GET", "/rooms/1", nil, nil},
	{"room accepts gzip", "GET", "/rooms/1", map[string]string{"Accept-Encoding": "gzip"}, nil},
	{"room accepts identity", "GET", "/rooms/1", map[string]string{"Accept-Encoding": "identity"}, nil},
	{"messages page", "GET", "/rooms/1/messages", nil, nil},
	{"messages before", "GET", "/rooms/1/messages?before=5", nil, nil},
	{"sidebar", "GET", "/users/sidebar", nil, nil},
	{"head room", "HEAD", "/rooms/1", nil, nil},
	{"post message", "POST", "/rooms/1/messages", map[string]string{"Content-Type": "text/plain"}, []byte("hello world")},
	{"unknown path", "GET", "/nope", nil, nil},
	{"unknown method", "DELETE", "/rooms/1", nil, nil},
}

// exchange is one observed response.
type exchange struct {
	status int
	header http.Header
	body   []byte
}

func testClient() *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func fetch(t *testing.T, client *http.Client, base string, tc requestCase) exchange {
	t.Helper()
	var body io.Reader
	if len(tc.body) > 0 {
		body = bytes.NewReader(tc.body)
	}
	request, err := http.NewRequest(tc.method, base+tc.path, body)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range tc.header {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return exchange{status: response.StatusCode, header: response.Header, body: payload}
}

// withoutVolatileHeaders drops Date, the one header two separate exchanges of
// the same request never share.
func withoutVolatileHeaders(header http.Header) http.Header {
	clone := header.Clone()
	clone.Del("Date")
	return clone
}

// TestPassThroughByteIdentical pins the strangler contract: with no owned
// routes the engine serves status, headers and body byte-identical to the
// legacy handler for a table of representative requests.
func TestPassThroughByteIdentical(t *testing.T) {
	legacy := legacyFixture()
	for _, mode := range []Mode{ModeOn, ModeForce, ModeOff} {
		t.Run(mode.String(), func(t *testing.T) {
			legacyServer := httptest.NewServer(legacy)
			defer legacyServer.Close()
			engine := New(legacy, Config{Mode: mode})
			engineServer := httptest.NewServer(engine)
			defer engineServer.Close()
			client := testClient()
			defer client.CloseIdleConnections()

			for _, tc := range passThroughCases {
				t.Run(tc.name, func(t *testing.T) {
					got := fetch(t, client, engineServer.URL, tc)
					want := fetch(t, client, legacyServer.URL, tc)
					if got.status != want.status {
						t.Fatalf("status = %d, legacy = %d", got.status, want.status)
					}
					if !reflect.DeepEqual(withoutVolatileHeaders(got.header), withoutVolatileHeaders(want.header)) {
						t.Fatalf("headers differ\n engine: %v\n legacy: %v", withoutVolatileHeaders(got.header), withoutVolatileHeaders(want.header))
					}
					if !bytes.Equal(got.body, want.body) {
						t.Fatalf("body differs\n engine: %q\n legacy: %q", got.body, want.body)
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

	got := httptest.NewRecorder()
	engine.ServeHTTP(got, httptest.NewRequest("GET", "/definitely-unknown", nil))
	want := httptest.NewRecorder()
	legacy.ServeHTTP(want, httptest.NewRequest("GET", "/definitely-unknown", nil))

	if got.Code != http.StatusNotFound {
		t.Fatalf("engine status = %d, want 404", got.Code)
	}
	if got.Code != want.Code {
		t.Fatalf("engine status = %d, legacy = %d", got.Code, want.Code)
	}
	if !bytes.Equal(got.Body.Bytes(), want.Body.Bytes()) {
		t.Fatalf("engine body = %q, legacy = %q", got.Body.Bytes(), want.Body.Bytes())
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
			if tc.mode == ModeForce {
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

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  Mode
	}{
		{"", ModeOn},
		{"on", ModeOn},
		{"off", ModeOff},
		{"force", ModeForce},
		{"OFF", ModeOn},
		{"bogus", ModeOn},
	} {
		if got := ParseMode(tc.value); got != tc.want {
			t.Errorf("ParseMode(%q) = %v, want %v", tc.value, got, tc.want)
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
// legacy Deflate wrapper adding a second gzip layer.
func TestOwnedRouteOwnsEncoding(t *testing.T) {
	member := gzipMember(t, "engine body")
	legacy := front.Deflate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "legacy body")
	}))
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Write(member)
	})
	engine := New(legacy, Config{Mode: ModeForce})
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
}
