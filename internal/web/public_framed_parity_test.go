package web

// Full public-chain parity for the ENGINE-49 precomposed head lane: the real
// application served through the production public composition
// (PublicCompression, the response cache, Deflate) on the owned loop, with the
// front's capture wrapper transparent to the lane and with the lane hidden by
// an opaque writer. Raw wire bytes are compared with Date masked, so the status
// line, the header block in order, the framing (Content-Length vs chunked) and
// the body must all match.
//
// The chain here is the production composition minus `forward`, which only
// edits request headers and is response-neutral (see aux_parity_test.go). A
// probe between the loop and the chain counts the precomposed emissions that
// reach the writer, so engagement is measured, not assumed.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastserve"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// publicOpaqueWriter ends the precomposed-receiver walk while forwarding the
// plain ResponseWriter surface: the chain then runs its header-map path on the
// same loop.
type publicOpaqueWriter struct{ http.ResponseWriter }

func hidePrecomposed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(publicOpaqueWriter{w}, r)
	})
}

// seamProbe sits between the loop and the public chain and counts the
// precomposed emissions by status, forwarding them to the loop. It is a
// precomposed receiver itself, so the chain's walk finds it exactly as it would
// find the loop.
type seamProbe struct {
	mu     sync.Mutex
	status map[int]int
}

func newSeamProbe() *seamProbe { return &seamProbe{status: map[int]int{}} }

func (p *seamProbe) record(status int) {
	p.mu.Lock()
	p.status[status]++
	p.mu.Unlock()
}

func (p *seamProbe) count(status int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status[status]
}

func (p *seamProbe) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&probeWriter{ResponseWriter: w, probe: p}, r)
	})
}

type probeWriter struct {
	http.ResponseWriter
	probe *seamProbe
}

func (w *probeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *probeWriter) WritePrecomposed(status int, head []byte, body [][]byte) error {
	w.probe.record(status)
	for {
		if receiver, ok := w.ResponseWriter.(interface {
			WritePrecomposed(int, []byte, [][]byte) error
		}); ok {
			return receiver.WritePrecomposed(status, head, body)
		}
		unwrapper, ok := w.ResponseWriter.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w.ResponseWriter = unwrapper.Unwrap()
	}
}

// publicChain is one side of the differential: the application, the probe that
// counted its emissions, and the loop address.
type publicChain struct {
	app   *Server
	probe *seamProbe
	addr  string
}

// chainFixture builds two applications over one database — the public chain
// with the precomposed lane reachable and with it hidden — plus a session and a
// room. cacheSize is the front response cache's capacity: a positive value
// exercises the miss path, zero the bypass path. zstd selects the recorded
// zstd frame variant, which changes the application's own encoding decision.
type chainFixture struct {
	on, off *Server
	cookie  *http.Cookie
	user    database.User
	room    database.Room
}

func chainFixtureFor(t *testing.T, zstd string) chainFixture {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "on")
	t.Setenv("CAMPFIRE_RECORDED_ZSTD", zstd)
	t.Setenv("CAMPFIRE_PRECOMPOSED_FRAMING", "on")
	t.Setenv("CAMPFIRE_REQUEST_ARENA", "on")
	// The whole-response cache would serve the second request of a case from
	// the stored body on the map path, hiding the precomposed lane; disabling
	// it forces a fresh render so the lane is exercised deterministically.
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("recorded-pieces")
	if err != nil {
		t.Fatal(err)
	}
	onApp, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(onApp.Close)
	offApp, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(offApp.Close)
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
	rooms, err := onApp.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := onApp.DB.CreateMessage(context.Background(), user.ID, rooms[0].ID, "parity seed", "<p>parity seed</p>", "parity seed"); err != nil {
		t.Fatal(err)
	}
	return chainFixture{
		on:     onApp,
		off:    offApp,
		cookie: &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)},
		user:   user,
		room:   rooms[0],
	}
}

// composePublic builds the production public composition over one application.
func composePublic(app http.Handler, cacheSize int64) http.Handler {
	cfg := front.Config{Gzip: true, CompressionJitter: 32, LogRequests: false, CacheSize: cacheSize, MaxCacheItemSize: cacheSize}
	cache := front.NewCache(cfg.CacheSize, cfg.MaxCacheItemSize)
	cache.FixedRoutes = true
	return front.PublicCompression(cache.Handler(front.Deflate(app)), cfg)
}

// testPublicChainPair serves the fixture on two owned loops: the lane reachable
// and the lane hidden by an opaque writer.
func testPublicChainPair(t *testing.T, cacheSize int64) (on, off publicChain, cookie *http.Cookie, user database.User) {
	return testPublicChainPairZstd(t, cacheSize, "off")
}

// testPublicChainPairZstd is testPublicChainPair with the recorded zstd frame
// variant configurable.
func testPublicChainPairZstd(t *testing.T, cacheSize int64, zstd string) (on, off publicChain, cookie *http.Cookie, user database.User) {
	t.Helper()
	fixture := chainFixtureFor(t, zstd)
	on = publicChain{app: fixture.on, probe: newSeamProbe()}
	on.addr = servePublicLoop(t, on.probe.wrap(composePublic(fixture.on, cacheSize)))
	off = publicChain{app: fixture.off, probe: newSeamProbe()}
	off.addr = servePublicLoop(t, off.probe.wrap(composePublic(hidePrecomposed(fixture.off), cacheSize)))
	return on, off, fixture.cookie, fixture.user
}

// servePublicLoop runs one handler on a private owned loop and returns its
// address.
func servePublicLoop(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := fastserve.New(handler)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	go srv.Serve(listener)
	return listener.Addr().String()
}

// TestPublicChainPrecomposedParity is the strategy-1 contract on the miss path:
// with the front capture wrapper transparent, the precomposed lane must emit
// the same bytes as the map path. Every case runs twice (the first pass fills
// the piece and variant caches, the second is the lane), the lane is proved
// engaged by the probe (a 200 and a 304 emission on the on-side, none on the
// hidden side), and the application's assembly counter confirms the gzip render
// stopped taking the map path's compressed branch.
func TestPublicChainPrecomposedParity(t *testing.T) {
	on, off, cookie, user := testPublicChainPair(t, 64<<20)
	ctx := context.Background()
	rooms, err := on.app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	var anchor int64
	for i := 0; i < 3; i++ {
		message, err := on.app.DB.CreateMessage(ctx, user.ID, room.ID, "pc-"+strconv.Itoa(i), "<p>pc seed</p>", "pc seed")
		if err != nil {
			t.Fatal(err)
		}
		if anchor == 0 {
			anchor = message.ID
		}
	}
	path := "/rooms/" + strconv.FormatInt(room.ID, 10)
	messagesPath := path + "/messages"
	lastRoom := "last_room=" + strconv.FormatInt(room.ID, 10)

	// Warm both encodings of the room page on both chains, then prove the lane
	// is the active writer on the on-side: a fresh gzip render must emit a
	// precomposed 200 and must not run the map path's compressed assembly.
	for _, addr := range []string{on.addr, off.addr} {
		rawFetch(t, addr, "campfire.test", "GET", path, "gzip", "", cookie, lastRoom)
		rawFetch(t, addr, "campfire.test", "GET", path, "identity", "", cookie, lastRoom)
	}
	assemblies := on.app.recordedAssemblies.Load()
	etag := ""
	raw, status := rawFetch(t, on.addr, "campfire.test", "GET", path, "gzip", "", cookie, lastRoom)
	if status != 200 {
		t.Fatalf("warm gzip: status %d", status)
	}
	if on.probe.count(200) == 0 {
		t.Fatal("the precomposed lane was not engaged on the public chain")
	}
	if got := on.app.recordedAssemblies.Load(); got != assemblies {
		t.Fatalf("the framed gzip render ran the map path's assembly: %d", got-assemblies)
	}
	for _, line := range bytes.Split(maskedHead(raw), []byte("\r\n")) {
		if bytes.HasPrefix(line, []byte("Etag: ")) {
			etag = string(bytes.TrimPrefix(line, []byte("Etag: ")))
		}
	}
	if etag == "" {
		t.Fatal("no ETag on the warm response")
	}
	if off.probe.count(200) != 0 {
		t.Fatal("the hidden-lane chain emitted a precomposed response")
	}

	cases := []struct {
		label, method, path, accept, ifNoneMatch string
	}{
		{"gzip", "GET", path, "gzip", ""},
		{"identity", "GET", path, "identity", ""},
		{"absent", "GET", path, "", ""},
		{"wildcard", "GET", path, "*", ""},
		{"gzip refused", "GET", path, "gzip;q=0", ""},
		{"head gzip", "HEAD", path, "gzip", ""},
		{"head identity", "HEAD", path, "identity", ""},
		{"messages gzip", "GET", messagesPath, "gzip", ""},
		{"messages identity", "GET", messagesPath, "identity", ""},
		{"fresh gzip", "GET", path, "gzip", etag},
		{"fresh identity", "GET", path, "identity", etag},
		{"anchor gzip", "GET", path + "/@" + strconv.FormatInt(anchor, 10), "gzip", ""},
	}
	for round := 0; round < 2; round++ {
		for _, c := range cases {
			rawOn, statusOn := rawFetch(t, on.addr, "campfire.test", c.method, c.path, c.accept, c.ifNoneMatch, cookie, lastRoom)
			rawOff, statusOff := rawFetch(t, off.addr, "campfire.test", c.method, c.path, c.accept, c.ifNoneMatch, cookie, lastRoom)
			if statusOn != statusOff {
				t.Fatalf("%s/%d: status %d vs %d", c.label, round, statusOn, statusOff)
			}
			a, b := maskDate(rawOn), maskDate(rawOff)
			if !bytes.Equal(a, b) {
				at := 0
				for at < len(a) && at < len(b) && a[at] == b[at] {
					at++
				}
				os.WriteFile("/tmp/opencode/pc."+c.label+".on", a, 0o644)
				os.WriteFile("/tmp/opencode/pc."+c.label+".off", b, 0o644)
				t.Fatalf("%s/%d: wire differs at offset %d\n--- lane ---\n%q\n--- map ---\n%q", c.label, round, at, a[max(0, at-80):min(len(a), at+80)], b[max(0, at-80):min(len(b), at+80)])
			}
			t.Logf("%s/%d OK (%d bytes)", c.label, round, len(rawOn))
		}
	}
	// The lane served the 200s and the conditional 304s on the on-side; the
	// hidden chain served none.
	if on.probe.count(200) == 0 || on.probe.count(304) == 0 {
		t.Fatalf("lane emissions: 200=%d 304=%d", on.probe.count(200), on.probe.count(304))
	}
	if off.probe.count(200) != 0 || off.probe.count(304) != 0 {
		t.Fatalf("hidden chain emissions: 200=%d 304=%d", off.probe.count(200), off.probe.count(304))
	}
}

// TestPublicChainBypassMarkerParity pins the bypass path's marker on the owned
// loop: with the front response cache disabled every public request bypasses
// it, and a Range request bypasses a cache that is otherwise enabled. The
// application still frames the recorded page in both cases, so the front's
// X-Cache marker must reach the wire through the seam exactly as the map path
// emits it, with the body complete and the lane engaged.
func TestPublicChainBypassMarkerParity(t *testing.T) {
	for _, test := range []struct {
		name      string
		cacheSize int64
		rangeArg  string
		want      string
	}{
		{"cache_disabled", 0, "", "bypass"},
		{"range_bypass", 64 << 20, "bytes=0-3", "bypass"},
	} {
		t.Run(test.name, func(t *testing.T) {
			on, off, cookie, user := testPublicChainPair(t, test.cacheSize)
			ctx := context.Background()
			rooms, err := on.app.DB.Rooms(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			room := rooms[0]
			if _, err := on.app.DB.CreateMessage(ctx, user.ID, room.ID, "bypass", "<p>bypass</p>", "bypass"); err != nil {
				t.Fatal(err)
			}
			path := "/rooms/" + strconv.FormatInt(room.ID, 10)
			lastRoom := "last_room=" + strconv.FormatInt(room.ID, 10)

			// Warm the piece and variant caches so the lane is reachable on
			// the request under test.
			for _, addr := range []string{on.addr, off.addr} {
				rawFetch(t, addr, "campfire.test", "GET", path, "gzip", "", cookie, lastRoom)
			}
			for round := 0; round < 2; round++ {
				before := on.probe.count(200)
				rawOn, statusOn := rawFetchHeaders(t, on.addr, path, cookie, lastRoom, test.rangeArg)
				rawOff, statusOff := rawFetchHeaders(t, off.addr, path, cookie, lastRoom, test.rangeArg)
				if statusOn != statusOff {
					t.Fatalf("round %d: status %d vs %d", round, statusOn, statusOff)
				}
				if !bytes.Contains(rawOn, []byte("X-Cache: "+test.want+"\r\n")) {
					t.Fatalf("round %d: bypassed lane response lacks the X-Cache marker:\n%s", round, maskedHead(rawOn))
				}
				if got := on.probe.count(200); got == before {
					t.Fatalf("round %d: the lane did not frame the bypassed request", round)
				}
				if !bytes.Equal(maskDate(rawOn), maskDate(rawOff)) {
					t.Fatalf("round %d: bypassed lane vs map mismatch\n--- lane ---\n%s\n--- map ---\n%s", round, maskDate(rawOn), maskDate(rawOff))
				}
			}
			if off.probe.count(200) != 0 {
				t.Fatal("the hidden-lane chain emitted a precomposed response")
			}
		})
	}
}

// rawFetchHeaders is the raw exchange used here: one GET with the loadgen's
// header shape plus an optional Range, which bypasses an otherwise enabled
// front cache without changing the cookie shape.
func rawFetchHeaders(t *testing.T, addr, path string, cookie *http.Cookie, lastRoom, rangeArg string) ([]byte, int) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var request strings.Builder
	fmt.Fprintf(&request, "GET %s HTTP/1.1\r\nHost: campfire.test\r\nConnection: close\r\nAccept-Encoding: gzip\r\n", path)
	if rangeArg != "" {
		fmt.Fprintf(&request, "Range: %s\r\n", rangeArg)
	}
	fmt.Fprintf(&request, "Cookie: session_token=%s; %s\r\n\r\n", cookie.Value, lastRoom)
	if _, err := conn.Write([]byte(request.String())); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	lines := bytes.Split(head, []byte("\r\n"))
	status := 0
	fmt.Sscanf(string(lines[0]), "HTTP/1.1 %d", &status)
	return raw, status
}

// maskedHead returns the head block of a raw response.
func maskedHead(raw []byte) []byte {
	head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	return head
}

// expectation is what a zstd negotiation case must do with the precomposed
// lane: frame the response, or leave it to the map path.
type expectation int

const (
	mustFrame expectation = iota
	mustNotFrame
)

// TestPublicChainZstdNegotiationParity pins the negotiation cases the first
// harness omitted: a client that accepts only zstd, one that prefers zstd over
// gzip, and the mixed shapes, with the application's recorded zstd frame
// variant on and off. The public compression policy knows zstd and the
// application's own selector does not (unless the zstd variant is on), so a
// response the application renders as identity would be compressed by the
// policy on the map path; the lane must not serve it as identity bytes.
func TestPublicChainZstdNegotiationParity(t *testing.T) {
	for _, zstd := range []string{"off", "on"} {
		t.Run("zstd_"+zstd, func(t *testing.T) {
			on, off, cookie, user := testPublicChainPairZstd(t, 64<<20, zstd)
			ctx := context.Background()
			rooms, err := on.app.DB.Rooms(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			room := rooms[0]
			if _, err := on.app.DB.CreateMessage(ctx, user.ID, room.ID, "zstd", "<p>zstd seed</p>", "zstd seed"); err != nil {
				t.Fatal(err)
			}
			path := "/rooms/" + strconv.FormatInt(room.ID, 10)
			lastRoom := "last_room=" + strconv.FormatInt(room.ID, 10)
			// The expectations pin the gate on the response, not on a
			// predicted negotiation. With the recorded zstd variant off the
			// application renders identity for a zstd-preferring client while
			// the policy selected zstd, so those responses must take the map
			// path (the map path compresses the identity body; a framed one
			// would go out uncompressed). Multi-piece zstd shapes renegotiate
			// against gzip and identity, so zstd-only clients likewise take
			// the map path even when recorded zstd is enabled.
			cases := []struct {
				label, accept string
				off, on       expectation
			}{
				{"zstd_only", "zstd", mustNotFrame, mustNotFrame},
				{"zstd_only_gzip_rejected", "zstd, gzip;q=0", mustNotFrame, mustNotFrame},
				{"gzip_and_zstd", "gzip, zstd", mustFrame, mustFrame},
				{"zstd_preferred", "gzip;q=0.5, zstd;q=1", mustFrame, mustFrame},
				{"gzip_preferred", "gzip;q=1, zstd;q=0.5", mustFrame, mustFrame},
				{"zstd_identity", "zstd, identity", mustNotFrame, mustNotFrame},
			}
			want := func(c struct {
				label, accept string
				off, on       expectation
			}) expectation {
				if zstd == "on" {
					return c.on
				}
				return c.off
			}
			for round := 0; round < 2; round++ {
				for _, c := range cases {
					before := on.probe.count(200)
					rawOn, statusOn := rawFetch(t, on.addr, "campfire.test", "GET", path, c.accept, "", cookie, lastRoom)
					emitted := on.probe.count(200) != before
					switch want(c) {
					case mustFrame:
						if !emitted {
							t.Fatalf("%s/%d: the lane did not frame a response the policy passes through", c.label, round)
						}
					case mustNotFrame:
						if emitted {
							t.Fatalf("%s/%d: the lane framed a response the policy would compress", c.label, round)
						}
					}
					rawOff, statusOff := rawFetch(t, off.addr, "campfire.test", "GET", path, c.accept, "", cookie, lastRoom)
					if statusOn != statusOff {
						t.Fatalf("%s/%d: status %d vs %d", c.label, round, statusOn, statusOff)
					}
					a, b := maskDate(rawOn), maskDate(rawOff)
					if !bytes.Equal(a, b) {
						at := 0
						for at < len(a) && at < len(b) && a[at] == b[at] {
							at++
						}
						t.Fatalf("%s/%d: wire differs at offset %d (lane %d bytes, map %d bytes)\n--- lane ---\n%q\n--- map ---\n%q", c.label, round, at, len(a), len(b), a[max(0, at-80):min(len(a), at+80)], b[max(0, at-80):min(len(b), at+80)])
					}
					t.Logf("%s/%d OK (%d bytes)", c.label, round, len(rawOn))
				}
			}
			if off.probe.count(200) != 0 || off.probe.count(304) != 0 {
				t.Fatalf("hidden chain emissions: 200=%d 304=%d", off.probe.count(200), off.probe.count(304))
			}
		})
	}
}

// TestPublicChainBypassOnNetHTTP is the bypass regression on the base writer
// the TLS/ACME public listener uses (net/http, not the owned loop; the loop
// only serves the plain-HTTP listener). The front must not tell the
// application a precomposed response can be emitted when nothing downstream
// can emit it — the forwarding policy wrapper would swallow the body — so a
// cache-disabled chain and a Range-triggered bypass must both deliver the
// complete response through the map path.
func TestPublicChainBypassOnNetHTTP(t *testing.T) {
	fixture := chainFixtureFor(t, "off")
	path := "/rooms/" + strconv.FormatInt(fixture.room.ID, 10)
	lastRoom := "last_room=" + strconv.FormatInt(fixture.room.ID, 10)

	// The reference side hides the precomposed seam at the application level,
	// which is exactly the map path the gate must fall back to. Each case
	// builds its own pair so the Range case bypasses an *enabled* cache.
	servers := func(cacheSize int64) (on, off *httptest.Server) {
		on = httptest.NewServer(composePublic(fixture.on, cacheSize))
		off = httptest.NewServer(composePublic(hidePrecomposed(fixture.off), cacheSize))
		t.Cleanup(func() { on.Close(); off.Close() })
		return on, off
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	defer client.CloseIdleConnections()

	fetch := func(server *httptest.Server, rangeHeader string) (int, string, []byte) {
		request, err := http.NewRequest("GET", server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		// One host for both servers: the page embeds the request origin, so
		// the two httptest ports would otherwise render different bytes.
		request.Host = "campfire.test"
		request.Header.Set("Accept-Encoding", "gzip")
		request.Header.Set("Cookie", "session_token="+fixture.cookie.Value+"; "+lastRoom)
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header.Get("X-Cache"), body
	}

	for _, test := range []struct {
		name      string
		cacheSize int64
		rangeArg  string
	}{
		{"cache_disabled", 0, ""},
		{"range_bypass", 64 << 20, "bytes=0-3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			onServer, offServer := servers(test.cacheSize)
			assemblies := fixture.on.recordedAssemblies.Load()
			status, marker, body := fetch(onServer, test.rangeArg)
			if status != 200 {
				t.Fatalf("status %d", status)
			}
			if len(body) == 0 {
				t.Fatal("empty body: the chain advertised a receiver with no emitter")
			}
			if got := fixture.on.recordedAssemblies.Load(); got == assemblies {
				t.Fatal("the map path's compressed branch did not run, so the lane framed")
			}
			wantStatus, wantMarker, wantBody := fetch(offServer, test.rangeArg)
			if status != wantStatus || marker != wantMarker || !bytes.Equal(body, wantBody) {
				at := 0
				for at < len(body) && at < len(wantBody) && body[at] == wantBody[at] {
					at++
				}
				t.Fatalf("status/marker %d/%q vs %d/%q, body %d bytes vs %d, first diff at %d\n got %q\nwant %q", status, marker, wantStatus, wantMarker, len(body), len(wantBody), at, body[max(0, at-16):min(len(body), at+16)], wantBody[max(0, at-16):min(len(wantBody), at+16)])
			}
			if marker != "bypass" {
				t.Fatalf("X-Cache = %q, want bypass", marker)
			}
		})
	}
}
