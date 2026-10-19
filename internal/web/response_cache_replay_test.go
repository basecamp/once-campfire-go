package web

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
)

// This file is the direct-replay lever's contract (response_cache.go's
// replayCached/serveCached304). A whole-response-cache hit used to fall back
// to the map path — a header-value copy per stored header, the loop's header
// snapshot and sort, and a full copy of the stored body into the loop's write
// buffer. The replay lane stores the head block the fresh response emitted
// (with Content-Length and the front's X-Cache marker baked in) alongside the
// body and emits a hit through the same precomposed seam the fresh recorded
// path uses. These tests pin the wire bytes: a hit must equal a fresh render
// of the same request, through the owned loop and through the public front
// chain, for 200, HEAD and 304, identity and gzip.

// publicReplayChain composes the public listener's front chain (the response
// cache's capture plus the public compression policy) without front.Serve's
// listeners, so a test can serve it on the owned loop and read the raw wire.
func publicReplayChain(app http.Handler) http.Handler {
	config := front.FromLookup(func(string) (string, bool) { return "", false })
	config.SkipDeflate = true
	return front.PublicCompression(front.NewCache(config.CacheSize, config.MaxCacheItemSize).Handler(app), config)
}

// productionReplayChain is the production composition: the application wrapped
// in the legacy Deflate middleware (the engine's legacy handler is
// front.Deflate(app)), then the front's response cache and the public
// compression policy. front.Serve adds no second Deflate for a precomposed
// handler, which is why the inner middleware is the one that matters.
func productionReplayChain(app http.Handler) http.Handler {
	config := front.FromLookup(func(string) (string, bool) { return "", false })
	config.SkipDeflate = true
	return front.PublicCompression(front.NewCache(config.CacheSize, config.MaxCacheItemSize).Handler(front.Deflate(app)), config)
}

// replayChains are the chains every replay test runs: the owned loop alone,
// the public chain alone, and the production composition of both.
func replayChains() []struct {
	name string
	mk   func(http.Handler) http.Handler
} {
	return []struct {
		name string
		mk   func(http.Handler) http.Handler
	}{
		{"owned-loop", front.Deflate},
		{"public-chain", publicReplayChain},
		{"production", productionReplayChain},
	}
}

// replayShape is one request shape the parity contract covers: the route, the
// negotiated encoding and the stable cookies a real browser repeats.
type replayShape struct {
	name   string
	path   string
	accept string
	extra  []string
}

func replayShapes(roomID string) []replayShape {
	return []replayShape{
		// The room route sets last_room when it is absent, so this shape is a
		// map-path response; the hit must reproduce that map head.
		{"room-identity", "/rooms/" + roomID, "identity", nil},
		{"room-gzip", "/rooms/" + roomID, "gzip", nil},
		// A returning browser repeats last_room, so no Set-Cookie is added and
		// the recorded response frames: this is the replay lane's hot shape.
		{"room-framed-identity", "/rooms/" + roomID, "identity", []string{"last_room=" + roomID}},
		{"room-framed-gzip", "/rooms/" + roomID, "gzip", []string{"last_room=" + roomID}},
		{"messages-identity", "/rooms/" + roomID + "/messages", "identity", nil},
		{"messages-gzip", "/rooms/" + roomID + "/messages", "gzip", nil},
		{"search-identity", "/searches?q=replay", "identity", nil},
		{"search-gzip", "/searches?q=replay", "gzip", nil},
	}
}

// replayEngages reports whether a shape's hit takes the precomposed replay
// lane. The room's non-framed shapes take the map path by design (the
// last_room Set-Cookie must reach the head); the framed room, messages and
// search shapes replay for both GET and HEAD (the recorded HEAD is composed
// on the map path, but its head is the same entity head the GET stored, and
// the receiver suppresses the body). A HEAD 304 stays on the map path because
// the fresh framed 304 branch excludes HEAD.
func replayEngages(shape string) bool {
	return shape != "room-identity" && shape != "room-gzip"
}

// TestResponseCacheReplayWireParity is the direct-replay lever's byte-identity
// contract: for every shape, the cache-on hit and the cache-off fresh render
// are byte-identical (Date masked) on the raw wire, through the owned loop and
// through the public front chain. The hit counter must move for every hit and
// the replay counter must move wherever the lane is expected to engage, so a
// run that silently took the map path cannot pass as a replay.
func TestResponseCacheReplayWireParity(t *testing.T) {
	for _, chain := range replayChains() {
		t.Run(chain.name, func(t *testing.T) {
			on, off, cookie, user := testResponseCacheApps(t)
			roomID := seedReplayRoom(t, on, user)
			_, onAddr := serveLoop(t, chain.mk(on))
			_, offAddr := serveLoop(t, chain.mk(off))

			for _, shape := range replayShapes(roomID) {
				t.Run(shape.name, func(t *testing.T) {
					fill, status := rawFetch(t, onAddr, "campfire.test", "GET", shape.path, shape.accept, "", cookie, shape.extra...)
					if status != 200 {
						t.Fatalf("fill: status %d\n%s", status, fill)
					}
					etag := headerValue(fill, "Etag")
					if etag == "" {
						t.Fatalf("fill carries no validator:\n%s", fill)
					}
					// 200 and HEAD: the fresh render (cache disabled) and the
					// hit (cache enabled) must agree byte for byte.
					for _, method := range []string{"GET", "HEAD"} {
						fresh, freshStatus := rawFetch(t, offAddr, "campfire.test", method, shape.path, shape.accept, "", cookie, shape.extra...)
						hits0, _ := on.responses.counters()
						replays0 := on.responseReplays.Load()
						hit, hitStatus := rawFetch(t, onAddr, "campfire.test", method, shape.path, shape.accept, "", cookie, shape.extra...)
						hits1, misses1 := on.responses.counters()
						replays1 := on.responseReplays.Load()
						if freshStatus != 200 || hitStatus != 200 {
							t.Fatalf("%s: fresh %d hit %d", method, freshStatus, hitStatus)
						}
						if hits1 <= hits0 {
							t.Fatalf("%s: the request did not hit the response cache (hits %d -> %d, misses %d)", method, hits0, hits1, misses1)
						}
						if !bytes.Equal(maskDate(fresh), maskDate(hit)) {
							t.Fatalf("%s: hit differs from the fresh render:\nFRESH:\n%s\nHIT:\n%s", method, maskDate(fresh), maskDate(hit))
						}
						if replayEngages(shape.name) && replays1 <= replays0 {
							t.Fatalf("%s: the hit did not take the replay lane (replays %d -> %d)", method, replays0, replays1)
						}
					}
					// 304: the hit's conditional response must be the fresh
					// 304 byte for byte, bodyless.
					fresh304, freshStatus := rawFetch(t, offAddr, "campfire.test", "GET", shape.path, shape.accept, etag, cookie, shape.extra...)
					hit304, hitStatus := rawFetch(t, onAddr, "campfire.test", "GET", shape.path, shape.accept, etag, cookie, shape.extra...)
					if freshStatus != http.StatusNotModified || hitStatus != http.StatusNotModified {
						t.Fatalf("304: fresh %d hit %d\nFRESH:\n%s\nHIT:\n%s", freshStatus, hitStatus, fresh304, hit304)
					}
					if !bytes.Equal(maskDate(fresh304), maskDate(hit304)) {
						t.Fatalf("304: hit differs from the fresh render:\nFRESH:\n%s\nHIT:\n%s", maskDate(fresh304), maskDate(hit304))
					}
				})
			}
		})
	}
}

// TestResponseCacheHit304ReproducesFreshHead pins the flavor-aware cache policy
// and coding of conditional hits against fresh renders. Both must also retain
// the selected ETag; TestConditional304WireValidators checks that explicitly.
func TestResponseCacheHit304ReproducesFreshHead(t *testing.T) {
	on, off, cookie, user := testResponseCacheApps(t)
	roomID := seedReplayRoom(t, on, user)
	_, onAddr := serveLoop(t, front.Deflate(on))
	_, offAddr := serveLoop(t, front.Deflate(off))

	for _, shape := range []replayShape{
		{"messages-gzip", "/rooms/" + roomID + "/messages", "gzip", nil},
		{"room-gzip", "/rooms/" + roomID, "gzip", nil},
		{"room-framed-gzip", "/rooms/" + roomID, "gzip", []string{"last_room=" + roomID}},
		{"search-gzip", "/searches?q=replay", "gzip", nil},
	} {
		t.Run(shape.name, func(t *testing.T) {
			fill, _ := rawFetch(t, onAddr, "campfire.test", "GET", shape.path, shape.accept, "", cookie, shape.extra...)
			etag := headerValue(fill, "Etag")
			if etag == "" {
				t.Fatalf("fill carries no validator:\n%s", fill)
			}
			fresh, freshStatus := rawFetch(t, offAddr, "campfire.test", "GET", shape.path, shape.accept, etag, cookie, shape.extra...)
			hits0, _ := on.responses.counters()
			hit, hitStatus := rawFetch(t, onAddr, "campfire.test", "GET", shape.path, shape.accept, etag, cookie, shape.extra...)
			hits1, _ := on.responses.counters()
			if freshStatus != http.StatusNotModified || hitStatus != http.StatusNotModified {
				t.Fatalf("status: fresh %d hit %d", freshStatus, hitStatus)
			}
			if hits1 <= hits0 {
				t.Fatalf("the conditional request did not hit the response cache (hits %d -> %d)", hits0, hits1)
			}
			if !bytes.Equal(maskDate(fresh), maskDate(hit)) {
				t.Fatalf("304 differs from the fresh render:\nFRESH:\n%s\nHIT:\n%s", maskDate(fresh), maskDate(hit))
			}
		})
	}
}

// TestResponseCacheReplayEngages pins the lane's engagement on the hot shape:
// a returning browser's messages-page 200/HEAD hits are emitted through the
// precomposed seam and move the replay counter, as does its recorded 304.
// Freshness now follows rendering instead of a timestamp-only early check.
// The non-framed room shape (a
// last_room Set-Cookie must reach the head) deliberately does not replay.
func TestResponseCacheReplayEngages(t *testing.T) {
	app, _, cookie, user := testResponseCacheApps(t)
	roomID := seedReplayRoom(t, app, user)
	_, addr := serveLoop(t, front.Deflate(app))
	path := "/rooms/" + roomID + "/messages"

	fill, status := rawFetch(t, addr, "campfire.test", "GET", path, "gzip", "", cookie)
	if status != 200 {
		t.Fatalf("fill: status %d", status)
	}
	etag := headerValue(fill, "Etag")
	for _, c := range []struct {
		label       string
		method      string
		ifNoneMatch string
		wantReplay  bool
	}{
		{"200", "GET", "", true},
		{"HEAD", "HEAD", "", true},
		{"304", "GET", etag, true},
	} {
		replays0 := app.responseReplays.Load()
		hits0, _ := app.responses.counters()
		_, status := rawFetch(t, addr, "campfire.test", c.method, path, "gzip", c.ifNoneMatch, cookie)
		replays1 := app.responseReplays.Load()
		hits1, _ := app.responses.counters()
		if status != 200 && status != http.StatusNotModified {
			t.Fatalf("%s: status %d", c.label, status)
		}
		if hits1 <= hits0 {
			t.Fatalf("%s: no cache hit (hits %d -> %d)", c.label, hits0, hits1)
		}
		if got := replays1 > replays0; got != c.wantReplay {
			t.Fatalf("%s: replay lane engaged=%v, want %v (replays %d -> %d)", c.label, got, c.wantReplay, replays0, replays1)
		}
	}
	// The room's non-framed shape keeps the map path: its Set-Cookie must be
	// emitted, so the replay lane refuses it.
	roomPath := "/rooms/" + roomID
	rawFetch(t, addr, "campfire.test", "GET", roomPath, "gzip", "", cookie)
	replays0 := app.responseReplays.Load()
	rawFetch(t, addr, "campfire.test", "GET", roomPath, "gzip", "", cookie)
	if replays := app.responseReplays.Load(); replays != replays0 {
		t.Fatalf("the Set-Cookie room shape took the replay lane (replays %d -> %d)", replays0, replays)
	}
	// The recorded routes' 304 does replay.
	searchFill, _ := rawFetch(t, addr, "campfire.test", "GET", "/searches?q=replay", "gzip", "", cookie)
	searchEtag := headerValue(searchFill, "Etag")
	replays0 = app.responseReplays.Load()
	_, status = rawFetch(t, addr, "campfire.test", "GET", "/searches?q=replay", "gzip", searchEtag, cookie)
	if status != http.StatusNotModified {
		t.Fatalf("search 304: status %d", status)
	}
	if replays := app.responseReplays.Load(); replays <= replays0 {
		t.Fatalf("the recorded 304 did not take the replay lane (replays %d -> %d)", replays0, replays)
	}
}

// TestResponseCacheReplayDisabledBaseline is the rollback comparison: with the
// response cache disabled the rollback switch changes nothing on the wire and
// the replay lane never engages, and the enabled hit is byte-identical to that
// disabled baseline.
func TestResponseCacheReplayDisabledBaseline(t *testing.T) {
	on, off, cookie, user := testResponseCacheApps(t)
	roomID := seedReplayRoom(t, on, user)
	_, onAddr := serveLoop(t, front.Deflate(on))
	_, offAddr := serveLoop(t, front.Deflate(off))
	path := "/rooms/" + roomID + "/messages"

	first, _ := rawFetch(t, offAddr, "campfire.test", "GET", path, "gzip", "", cookie)
	second, _ := rawFetch(t, offAddr, "campfire.test", "GET", path, "gzip", "", cookie)
	if !bytes.Equal(maskDate(first), maskDate(second)) {
		t.Fatal("repeated renders differ with the response cache disabled")
	}
	if hits, _ := off.responses.counters(); hits != 0 {
		t.Fatalf("budget-zero cache reported %d hits", hits)
	}
	if replays := off.responseReplays.Load(); replays != 0 {
		t.Fatalf("budget-zero cache reported %d replays", replays)
	}
	rawFetch(t, onAddr, "campfire.test", "GET", path, "gzip", "", cookie)
	hit, _ := rawFetch(t, onAddr, "campfire.test", "GET", path, "gzip", "", cookie)
	if !bytes.Equal(maskDate(first), maskDate(hit)) {
		t.Fatalf("the hit differs from the cache-disabled baseline:\nBASELINE:\n%s\nHIT:\n%s", maskDate(first), maskDate(hit))
	}
}

// keepAlive drives several requests over ONE connection. A response that
// leaves the connection unusable — the server closes it, or the body framing
// desyncs — fails the next fetch, which is exactly the symptom the
// fresh-connection parity tests cannot see.
type keepAlive struct {
	t    *testing.T
	conn net.Conn
	buf  []byte
}

func dialKeepAlive(t *testing.T, addr string) *keepAlive {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &keepAlive{t: t, conn: conn}
}

// read appends whatever is available to the buffer, reporting whether the
// connection is still usable.
func (k *keepAlive) read() bool {
	k.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var tmp [64 << 10]byte
	n, err := k.conn.Read(tmp[:])
	if n > 0 {
		k.buf = append(k.buf, tmp[:n]...)
	}
	return err == nil || n > 0
}

// fetch sends one keep-alive request and returns the complete raw response.
func (k *keepAlive) fetch(method, path, accept, ifNoneMatch, cookie string, extra ...string) []byte {
	k.t.Helper()
	var req strings.Builder
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: campfire.test\r\n", method, path)
	if accept != "" {
		fmt.Fprintf(&req, "Accept-Encoding: %s\r\n", accept)
	}
	if ifNoneMatch != "" {
		fmt.Fprintf(&req, "If-None-Match: %s\r\n", ifNoneMatch)
	}
	if cookie != "" {
		fmt.Fprintf(&req, "Cookie: session_token=%s", cookie)
		for _, e := range extra {
			fmt.Fprintf(&req, "; %s", e)
		}
		req.WriteString("\r\n")
	}
	req.WriteString("\r\n")
	if _, err := k.conn.Write([]byte(req.String())); err != nil {
		k.t.Fatalf("%s %s: write to the keep-alive connection failed: %v", method, path, err)
	}
	for !bytes.Contains(k.buf, []byte("\r\n\r\n")) {
		if !k.read() {
			k.t.Fatalf("%s %s: the keep-alive connection was closed before the response head: the previous response left it unusable", method, path)
		}
	}
	at := bytes.Index(k.buf, []byte("\r\n\r\n"))
	head := k.buf[:at]
	rest := k.buf[at+4:]
	if bytes.Contains(bytes.ToLower(head), []byte("transfer-encoding:")) {
		k.t.Fatalf("%s %s: chunked framing on a shape whose framing is explicit:\n%s", method, path, head)
	}
	// A HEAD response carries the GET length but no body; a 304 (and any
	// bodyless status) carries no body either, and its head need not declare a
	// length.
	var code int
	fmt.Sscanf(string(head), "HTTP/1.1 %d", &code)
	bodyless := method == "HEAD" || code == http.StatusNotModified || code == http.StatusNoContent || code < 200
	length := 0
	if !bodyless {
		parsed, err := strconv.Atoi(headerValue(head, "Content-Length"))
		if err != nil {
			k.t.Fatalf("%s %s: no Content-Length in the response head (at=%d, buffered=%d):\n%q", method, path, at, len(k.buf), k.buf[:min(len(k.buf), 400)])
		}
		length = parsed
	}
	for len(rest) < length {
		if !k.read() {
			k.t.Fatalf("%s %s: body short: %d of %d bytes, then the keep-alive connection closed", method, path, len(rest), length)
		}
		rest = k.buf[at+4:]
	}
	raw := append([]byte(nil), k.buf[:at+4+length]...)
	k.buf = rest[length:]
	return raw
}

// maskKeepAlive drops the two lines that legitimately differ between a
// keep-alive exchange and the fresh-connection reference: Date (wall clock)
// and Connection (the reference asks the server to close).
func maskKeepAlive(raw []byte) []byte {
	lines := bytes.Split(raw, []byte("\r\n"))
	out := lines[:0:0]
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("Date: ")) || bytes.HasPrefix(line, []byte("Connection: ")) {
			continue
		}
		out = append(out, line)
	}
	return bytes.Join(out, []byte("\r\n"))
}

// TestResponseCacheReplayKeepAlive is the regression the fresh-connection
// parity tests could not see. A replayed response used to leave the keep-alive
// connection unusable: the replay lane bypasses the Deflate middleware's
// WriteHeader, so unless the response's wire coding is on the header map (the
// line the fresh framed path leaves there) that middleware starts its own
// gzip writer after the response is already on the wire; at close the writer
// emits a lone gzip member, the owned loop's Content-Length guard rejects it
// and marks the connection broken, and the server closes the connection
// without a Connection: close header. The client's next request then fails,
// which a load generator counts as an error on every other request.
//
// Every request below travels over ONE connection, so a shape that leaves the
// connection unusable fails the following fetch: the test fails on the pre-fix
// tree and passes after it. The shapes run in the order the failure was
// reported (messages gzip first), then room and search, identity and gzip, and
// HEAD/304.
func TestResponseCacheReplayKeepAlive(t *testing.T) {
	for _, chain := range replayChains() {
		t.Run(chain.name, func(t *testing.T) {
			on, off, cookie, user := testResponseCacheApps(t)
			roomID := seedReplayRoom(t, on, user)
			_, onAddr := serveLoop(t, chain.mk(on))
			_, offAddr := serveLoop(t, chain.mk(off))
			messagesPath := "/rooms/" + roomID + "/messages"
			roomPath := "/rooms/" + roomID
			searchPath := "/searches?q=replay"
			lastRoom := []string{"last_room=" + roomID}

			shapes := []struct {
				label       string
				method      string
				path        string
				accept      string
				extra       []string
				conditional bool
			}{
				{"messages-gzip", "GET", messagesPath, "gzip", nil, false},
				{"messages-identity", "GET", messagesPath, "identity", nil, false},
				{"messages-head-gzip", "HEAD", messagesPath, "gzip", nil, false},
				{"messages-304-gzip", "GET", messagesPath, "gzip", nil, true},
				{"room-gzip", "GET", roomPath, "gzip", nil, false},
				{"room-framed-gzip", "GET", roomPath, "gzip", lastRoom, false},
				{"room-framed-304-gzip", "GET", roomPath, "gzip", lastRoom, true},
				{"search-gzip", "GET", searchPath, "gzip", nil, false},
				{"search-identity", "GET", searchPath, "identity", nil, false},
				{"search-304-gzip", "GET", searchPath, "gzip", nil, true},
				{"messages-again-gzip", "GET", messagesPath, "gzip", nil, false},
			}

			conn := dialKeepAlive(t, onAddr)
			totalReplays := int64(0)
			for _, s := range shapes {
				fill := conn.fetch(s.method, s.path, s.accept, "", cookie.Value, s.extra...)
				ifNoneMatch := ""
				if s.conditional {
					ifNoneMatch = headerValue(fill, "Etag")
					if ifNoneMatch == "" {
						t.Fatalf("%s: the fill carries no validator", s.label)
					}
				}
				hits0, _ := on.responses.counters()
				replays0 := on.responseReplays.Load()
				hit := conn.fetch(s.method, s.path, s.accept, ifNoneMatch, cookie.Value, s.extra...)
				hits1, _ := on.responses.counters()
				replays1 := on.responseReplays.Load()
				totalReplays += replays1 - replays0
				// Room without last_room changes the session and deliberately
				// takes the map path. All other shapes here must replay.
				if s.label != "room-gzip" && replays1-replays0 != 1 {
					t.Fatalf("%s: replay delta %d, want 1", s.label, replays1-replays0)
				}
				if hits1 <= hits0 {
					t.Fatalf("%s: the second request on the connection did not hit the response cache (hits %d -> %d)", s.label, hits0, hits1)
				}
				if got := headerValue(hit, "Connection"); got == "close" {
					t.Fatalf("%s: the response asked for the connection to close", s.label)
				}
				fresh, freshStatus := rawFetch(t, offAddr, "campfire.test", s.method, s.path, s.accept, ifNoneMatch, cookie, s.extra...)
				want := 200
				if s.conditional {
					want = http.StatusNotModified
				}
				if freshStatus != want {
					t.Fatalf("%s: the fresh reference returned %d, want %d", s.label, freshStatus, want)
				}
				if !bytes.Equal(maskKeepAlive(hit), maskKeepAlive(fresh)) {
					t.Fatalf("%s: the keep-alive hit differs from the fresh render:\nHIT:\n%s\nFRESH:\n%s", s.label, maskKeepAlive(hit), maskKeepAlive(fresh))
				}
			}
			// The reported failing shape is the messages page's gzip hit; the
			// sequence must have replayed, and the connection must still be
			// usable for an uncached route at the very end.
			if totalReplays == 0 {
				t.Fatal("no request in the keep-alive sequence took the replay lane")
			}
			up := conn.fetch("GET", "/up", "", "", cookie.Value)
			if headerValue(up, "Content-Length") == "" {
				t.Fatalf("the keep-alive connection did not survive the sequence:\n%s", up)
			}
			t.Logf("%d replay-lane responses over one keep-alive connection", totalReplays)
		})
	}
}

// seedReplayRoom creates one message in the user's first room so every shape
// renders a populated page, and returns the room id as a path segment.
func seedReplayRoom(t *testing.T, app *Server, user database.User) string {
	t.Helper()
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("fixture has no rooms: %v", err)
	}
	if _, err := app.DB.CreateMessage(context.Background(), user.ID, rooms[0].ID, "", "<p>replay seed message</p>", "replay seed message"); err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(rooms[0].ID, 10)
}
