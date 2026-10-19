package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastserve"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// The direct-replay lever's measurement. BenchmarkResponseCacheReplay drives
// the real public chain (PublicCompression -> the front response cache -> the
// application) on the owned loop over a real keep-alive connection, and
// asserts, before the timer starts, that each request engaged both the
// whole-response cache and the precomposed replay lane: a run that silently
// took the map path cannot be reported as a replay. Its allocation numbers
// include the loop, net/http's client and the application's own per-request
// work, so the lever's zero-allocation claim is proved separately by
// BenchmarkResponseCacheReplayHandler, which runs the hit path in process
// against a stub precomposed receiver.
//
// The loop is not incidental: the public chain's precomposed seam exists only
// when the writer it wraps is the owned loop's response. A net/http listener
// advertises no emitter, so the front's capture stays opaque and the replay
// lane is off there by design (precomposed.go's emitterIn).

// benchReplayCase is one timed shape: the request method and whether it is
// conditional on the route's stored validator.
type benchReplayCase struct {
	name        string
	method      string
	conditional bool
}

var benchReplayCases = []benchReplayCase{
	{"200", "GET", false},
	{"head", "HEAD", false},
	{"304", "GET", true},
}

type benchReplayRoute struct{ name, path string }

func benchReplayRoutes(roomID string) []benchReplayRoute {
	return []benchReplayRoute{
		{"room", "/rooms/" + roomID},
		{"messages", "/rooms/" + roomID + "/messages"},
		{"search", "/searches?q=benchmarkneedle"},
	}
}

// benchReplayFixture builds the populated application (one room with forty
// messages carrying the benchmark word), serves it through the public chain on
// the owned loop, and returns the app, the base URL, the room id, the stable
// cookie and each route's stored validator. The cookie repeats last_room so
// the room route takes the framed (replay-eligible) shape a returning browser
// gets.
func benchReplayFixture(b *testing.B) (*Server, string, string, string, map[string]string) {
	b.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previous) })
	// The checkpointer's PASSIVE checkpoint can move the observed generation
	// mid-benchmark and invalidate the whole-response cache; push it past the
	// run so the measured path is the warm one.
	b.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	b.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := b.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("replay-bench")
	if err != nil {
		b.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(app.Close)
	ctx := context.Background()
	user, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		b.Fatal(err)
	}
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		b.Fatalf("fixture has no rooms: %v", err)
	}
	room := rooms[0]
	for i := 0; i < 40; i++ {
		body := fmt.Sprintf("<p>benchmarkneedle message %d</p>", i)
		if _, err := db.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("replay-bench-%d", i), body, fmt.Sprintf("benchmarkneedle %d", i)); err != nil {
			b.Fatal(err)
		}
	}
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		b.Fatal(err)
	}
	// The cookie is signed on the database's clock, so the auth middleware
	// does not treat the session as due for a refresh (which would change the
	// browser session and keep the response off the framed path).
	signed, err := secrets.SignCookie("session_token", token, db.Now().Add(time.Hour))
	if err != nil {
		b.Fatal(err)
	}
	roomID := strconv.FormatInt(room.ID, 10)
	cookie := "session_token=" + rails.EscapeCookie(signed) + "; last_room=" + roomID
	baseURL := benchServeLoop(b, publicReplayChain(app))

	client := benchReplayClient(b)
	etags := map[string]string{}
	for _, route := range benchReplayRoutes(roomID) {
		response, err := benchReplayDo(client, baseURL, route.path, cookie, "gzip", "GET", "")
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			b.Fatalf("%s fill: status %d", route.name, response.StatusCode)
		}
		etags[route.name] = response.Header.Get("ETag")
		if etags[route.name] == "" {
			b.Fatalf("%s fill carries no validator", route.name)
		}
	}
	return app, baseURL, roomID, cookie, etags
}

// benchServeLoop runs the owned loop on an ephemeral port for a benchmark and
// returns the base URL the client dials.
func benchServeLoop(b *testing.B, handler http.Handler) string {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	server := fastserve.New(handler)
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	})
	go server.Serve(listener)
	return "http://" + listener.Addr().String()
}

// benchReplayClient is one keep-alive connection, the shape a warm read
// arrives on.
func benchReplayClient(b *testing.B) *http.Client {
	b.Helper()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	return client
}

func benchReplayDo(client *http.Client, baseURL, path, cookie, accept, method, etag string) (*http.Response, error) {
	request, err := http.NewRequest(method, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cookie", cookie)
	if accept != "" {
		request.Header.Set("Accept-Encoding", accept)
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	return client.Do(request)
}

// BenchmarkResponseCacheReplay is the lever's end-to-end measurement: the warm
// hit for every route, encoding and method shape through the public chain on
// the owned loop. Each sub-benchmark proves its engagement (the hit counter
// moved) and its lane (the replay counter moved) before the timer starts.
// Timings on a busy host are noisy; the counts and the in-process allocation
// benchmark are the load-independent evidence.
func BenchmarkResponseCacheReplay(b *testing.B) {
	app, baseURL, roomID, cookie, etags := benchReplayFixture(b)
	for _, route := range benchReplayRoutes(roomID) {
		for _, accept := range []string{"identity", "gzip"} {
			for _, c := range benchReplayCases {
				b.Run(route.name+"/"+accept+"/"+c.name, func(b *testing.B) {
					client := benchReplayClient(b)
					etag := ""
					if c.conditional {
						etag = etags[route.name]
					}
					// Warm: the first request fills or serves the warm entry,
					// the second is the measured shape.
					benchReplayRun(b, client, baseURL, route.path, cookie, accept, c, etag)
					hits0, _ := app.responses.counters()
					replays0 := app.responseReplays.Load()
					benchReplayRun(b, client, baseURL, route.path, cookie, accept, c, etag)
					hits1, misses1 := app.responses.counters()
					replays1 := app.responseReplays.Load()
					if hits1 <= hits0 {
						b.Fatalf("%s/%s/%s did not hit the response cache (hits %d -> %d, misses %d)", route.name, accept, c.name, hits0, hits1, misses1)
					}
					// The messages page composes its fresh 304 on the map path
					// (its validator answers before rendering), so its 304 does
					// not take the lane; every other shape does.
					wantReplay := !(c.conditional && route.name == "messages")
					if wantReplay && replays1 <= replays0 {
						b.Fatalf("%s/%s/%s did not take the replay lane (replays %d -> %d)", route.name, accept, c.name, replays0, replays1)
					}
					if !wantReplay && replays1 != replays0 {
						b.Fatalf("%s/%s/%s took the replay lane (replays %d -> %d)", route.name, accept, c.name, replays0, replays1)
					}
					b.ReportAllocs()
					b.ResetTimer()
					b.ReportMetric(float64(replays1-replays0), "warm_replays")
					for i := 0; i < b.N; i++ {
						benchReplayRun(b, client, baseURL, route.path, cookie, accept, c, etag)
					}
				})
			}
		}
	}
}

// benchReplayRun issues one shape and checks the status the wire must carry.
func benchReplayRun(b *testing.B, client *http.Client, baseURL, path, cookie, accept string, c benchReplayCase, etag string) int {
	b.Helper()
	response, err := benchReplayDo(client, baseURL, path, cookie, accept, c.method, etag)
	if err != nil {
		b.Fatal(err)
	}
	length, err := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil {
		b.Fatal(err)
	}
	want := 200
	if c.conditional {
		want = http.StatusNotModified
	}
	if response.StatusCode != want {
		b.Fatalf("%s %s: status %d, want %d", c.method, path, response.StatusCode, want)
	}
	return int(length)
}

// replayProbeWriter models the owned loop's precomposed receiver for the
// in-process hit benchmark: it records which surface the hit used and
// allocates nothing per call.
type replayProbeWriter struct {
	header      http.Header
	precomposed int
	plain       int
	status      int
	head        []byte
	parts       [][]byte
}

func (w *replayProbeWriter) Header() http.Header { return w.header }
func (w *replayProbeWriter) WriteHeader(int)     {}
func (w *replayProbeWriter) Write(p []byte) (int, error) {
	w.plain++
	return len(p), nil
}
func (w *replayProbeWriter) WritePrecomposed(status int, head []byte, parts [][]byte) error {
	w.precomposed++
	w.status, w.head, w.parts = status, head, parts
	return nil
}

// BenchmarkResponseCacheReplayHandler proves the lever's zero-allocation claim
// on the web side: a warm hit's serveCached + finish run against a stub
// precomposed receiver, for 200, HEAD and 304 in both encodings, and the
// allocation count is asserted (not just reported). The entry is the one the
// real admission stored for the search page through the public chain — a
// recorded-flavor entry, whose 200/HEAD/304 all take the lane (the messages
// page's 304 is composed on the map path; see serveCached304) — so the head
// block, body and 304 flavor are exactly production's.
func BenchmarkResponseCacheReplayHandler(b *testing.B) {
	app, baseURL, _, cookie, _ := benchReplayFixture(b)
	path := "/searches?q=benchmarkneedle"
	client := benchReplayClient(b)
	for _, accept := range []string{"identity", "gzip"} {
		// Warm the entry for this encoding: the fixture filled only gzip.
		benchReplayRun(b, client, baseURL, path, cookie, accept, benchReplayCases[0], "")
		entry := findReplayEntry(b, app, path, accept)
		etag := string(entry.etag)
		for _, c := range benchReplayCases {
			b.Run(accept+"/"+c.name, func(b *testing.B) {
				writer := &replayProbeWriter{header: http.Header{}}
				// The front's cache middleware sets the marker once per
				// request; here it is set once for the whole benchmark, so the
				// measured path is the replay's own work.
				if entry.xcache != "" {
					writer.header.Set("X-Cache", entry.xcache)
				}
				request := httptest.NewRequest(c.method, path, nil)
				if c.conditional {
					request.Header.Set("If-None-Match", etag)
				}
				run := func() {
					arena := borrowRequestArena()
					buffered := borrowResponseBuffer(writer, arena)
					buffered.framed = true
					app.serveCached(entry, buffered, request)
					buffered.finish(request)
					releaseResponseBuffer(buffered)
					releaseRequestArena(arena)
				}
				run() // warm the pools and the probe
				if writer.precomposed == 0 || writer.plain != 0 {
					b.Fatalf("the hit did not use the precomposed seam (precomposed %d, plain %d)", writer.precomposed, writer.plain)
				}
				if allocs := testing.AllocsPerRun(200, run); allocs != 0 {
					b.Fatalf("the replay hit allocates %.2f objects per call, want 0", allocs)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					run()
				}
			})
		}
	}
}

// findReplayEntry returns the cached entry for path in the given wire
// encoding. The stored key length-prefixes each field, so ":<path>|" names the
// request URI exactly — a plain substring match would confuse /rooms/1 with
// /rooms/1/messages.
func findReplayEntry(b *testing.B, app *Server, path, accept string) *cachedResponse {
	b.Helper()
	want := ""
	if accept == "gzip" {
		want = "gzip"
	}
	marker := []byte(":" + path + "|")
	app.responses.mu.Lock()
	defer app.responses.mu.Unlock()
	for element := app.responses.order.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*cachedResponse)
		if bytes.Contains(entry.key, marker) && entry.header.Get("Content-Encoding") == want {
			return entry
		}
	}
	b.Fatalf("no cached %s entry for %s", accept, path)
	return nil
}
