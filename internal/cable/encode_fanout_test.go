// ENGINE-57 tests: byte-exactness of the once-escaped frame encoding against
// the pre-ENGINE-57 json.Marshal wrapper, pointer-identity sharing across
// subscribers and publishes, the zero-allocation steady-state pin, and
// concurrent mixed publishes delivering exact payload sets.
package cable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// escapeCorpus exercises every JSON string-escaping case the frame encoder
// must byte-match against encoding/json's HTML-safe escaping: short escapes,
// controls, HTML characters, the JS separators, invalid UTF-8, unicode, the
// empty string, and payloads beyond the pooled scratch's initial capacity.
var escapeCorpus = []string{
	`<turbo-stream action="append"><template>hello <b>world</b> &amp; "quoted" \back\</template></turbo-stream>`,
	"tab\tnewline\ncr\rbell\bformfeed\fctl\x01\x1f",
	"☃ emoji 🎉 multi-byte and ascii",
	"\u2028line sep\u2029para sep",
	"invalid: \xff\xfe and truncated \xc3",
	"",
	"plain ascii payload",
	strings.Repeat("x", 4096), // grows past the pooled scratch's 1 KiB start
	strings.Repeat("x", 4096) + "\u2028" + strings.Repeat("y", 4096),
}

// legacyFrameBytes is the pre-ENGINE-57 wrapper encoding: json.Marshal of the
// {identifier, message} struct the hub used to build per identifier.
func legacyFrameBytes(identifier string, message any) []byte {
	data, err := json.Marshal(struct {
		Identifier string `json:"identifier"`
		Message    any    `json:"message"`
	}{identifier, message})
	if err != nil {
		panic(err)
	}
	return data
}

func TestAppendJSONStringMatchesMarshal(t *testing.T) {
	for _, s := range escapeCorpus {
		got := appendJSONString(nil, s)
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Logf("got hex:  %x", got)
			t.Logf("want hex: %x", want)
			t.Fatalf("escape mismatch for %q:\n got %q\nwant %q", s, got, want)
		}
	}
}

// TestPublishFrameBytesMatchLegacy pins the wire frames of the shared encode
// to the pre-ENGINE-57 bytes for the same broadcasts: every corpus payload
// through hub.Publish and the map payload through hub.PublishStream must
// produce exactly the old json.Marshal wrapper bytes.
func TestPublishFrameBytesMatchLegacy(t *testing.T) {
	hub, db, user, roomID, _ := hubFixture(t)
	ctx := context.Background()
	token, err := db.StartSession(ctx, user.ID, "bytes", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	// Identifier containing every escapable character: identifiers are
	// client-supplied strings, so the frame encoder must escape them exactly
	// like the old struct marshal did.
	nastyIdent := `{"channel":"RoomMessagesChannel","signed_stream_name":"a\b"<c>&d` + "\u2028" + `e"}`
	c := simulateClient(t, user, token, nastyIdent, subscription{Channel: "RoomMessagesChannel", Room: roomID})
	hub.clients[c] = struct{}{}
	hub.mu.Unlock()

	for i, markup := range escapeCorpus {
		hub.Publish(ctx, roomID, markup)
		var f *websocket.PreparedMessage
		select {
		case f = <-c.out:
		case <-time.After(10 * time.Second):
			t.Fatalf("corpus %d: no delivery", i)
		}
		if !bytes.Equal(f.Data(), legacyFrameBytes(nastyIdent, markup)) {
			t.Fatalf("corpus %d frame bytes differ:\n got %q\nwant %q", i, f.Data(), legacyFrameBytes(nastyIdent, markup))
		}
	}

	// Non-string payloads: one shared json.Marshal spliced into the wrapper.
	msg := map[string]any{"action": "append", "target": "shared_rooms", "markup": "<div>&amp;</div>", "n": 7}
	ident := `{"channel":"Turbo::StreamsChannel","signed_stream_name":"abc"}`
	c2 := simulateClient(t, user, token, ident, subscription{Stream: "rooms"})
	hub.clients[c2] = struct{}{}
	hub.PublishStream(ctx, "rooms", msg)
	var f *websocket.PreparedMessage
	select {
	case f = <-c2.out:
	case <-time.After(10 * time.Second):
		t.Fatal("stream publish: no delivery")
	}
	if !bytes.Equal(f.Data(), legacyFrameBytes(ident, msg)) {
		t.Fatalf("map-payload frame bytes differ:\n got %q\nwant %q", f.Data(), legacyFrameBytes(ident, msg))
	}
}

// TestPublishSharedFramesAcrossSubscribersAndPublishes asserts the pointer
// identity contract: all subscribers of one (stream, identifier, payload)
// share one immutable frame, the same payload across publishes reuses the
// retained frame, and a different payload gets a different frame.
func TestPublishSharedFramesAcrossSubscribersAndPublishes(t *testing.T) {
	hub, db, user, roomID, _ := hubFixture(t)
	ctx := context.Background()
	token, err := db.StartSession(ctx, user.ID, "shared", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
	hub.mu.Lock()
	var clients []*client
	for i := 0; i < 5; i++ {
		c := simulateClient(t, user, token, identifier, subscription{Channel: "RoomMessagesChannel", Room: roomID})
		clients = append(clients, c)
		hub.clients[c] = struct{}{}
	}
	hub.mu.Unlock()

	markup := "<turbo-stream action=\"append\"><template>one</template></turbo-stream>"
	hub.Publish(ctx, roomID, markup)
	var first *websocket.PreparedMessage
	for _, c := range clients {
		select {
		case f := <-c.out:
			if first == nil {
				first = f
			} else if f != first {
				t.Fatal("subscribers of the same publish received different frames")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("missing delivery")
		}
	}
	// Same payload across publishes: the frame cache returns the retained
	// frame, so a later broadcast shares the pointer, too.
	hub.Publish(ctx, roomID, markup)
	select {
	case f := <-clients[0].out:
		if f != first {
			t.Fatal("same payload across publishes did not reuse the retained frame")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("missing delivery")
	}
	// Different payload: a distinct frame (exact-payload cache semantics).
	hub.Publish(ctx, roomID, "<turbo-stream action=\"append\"><template>two</template></turbo-stream>")
	select {
	case f := <-clients[0].out:
		if f == first {
			t.Fatal("different payload reused the same frame")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("missing delivery")
	}
}

// TestPublishStreamSharesOneFrame across members of a stream publish with a
// non-string payload: the map is encoded once and every member receives the
// identical frame.
func TestPublishStreamSharesOneFrame(t *testing.T) {
	hub, db, user, _, _ := hubFixture(t)
	ctx := context.Background()
	token, err := db.StartSession(ctx, user.ID, "mapshared", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	identifier := `{"channel":"Turbo::StreamsChannel","signed_stream_name":"rooms-signed"}`
	hub.mu.Lock()
	var clients []*client
	for i := 0; i < 4; i++ {
		c := simulateClient(t, user, token, identifier, subscription{Stream: "rooms"})
		clients = append(clients, c)
		hub.clients[c] = struct{}{}
	}
	hub.mu.Unlock()

	// Unread/push-style broadcast: one map payload, shared by every member
	// (no per-member map construction in the cable path; where content
	// genuinely differs per member — e.g. unread counts — the caller
	// publishes per-member streams with distinct payloads, one publish
	// each).
	msg := map[string]any{"roomId": 42, "markup": "<span>&amp;</span>"}
	hub.PublishStream(ctx, "rooms", msg)
	var first *websocket.PreparedMessage
	for _, c := range clients {
		select {
		case f := <-c.out:
			if first == nil {
				first = f
			} else if f != first {
				t.Fatal("stream members received different frames for one payload")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("missing delivery")
		}
	}
	var got struct {
		Identifier string         `json:"identifier"`
		Message    map[string]any `json:"message"`
	}
	if err := json.Unmarshal(first.Data(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Identifier != identifier || got.Message["roomId"] != float64(42) {
		t.Fatalf("unexpected shared frame %s", first.Data())
	}
}

// TestPublishMemoOverflowFallback exercises a publish fanning out over more
// distinct identifiers than the stack memo holds: the map fallback must
// deliver every identifier exactly, and cache reuse must hold for them too.
func TestPublishMemoOverflowFallback(t *testing.T) {
	hub, db, user, _, _ := hubFixture(t)
	ctx := context.Background()
	token, err := db.StartSession(ctx, user.ID, "memo", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	const n = frameMemoLen + 3 // 11 distinct identifiers, one per client
	hub.mu.Lock()
	var clients []*client
	for i := 0; i < n; i++ {
		ident := `{"channel":"RoomChannel","room_id":` + strconv.FormatInt(int64(i+1), 10) + `}`
		// Distinct identifier strings, one shared stream/room so the publish
		// fans out to all of them (the identifier's room_id is client-side
		// text; subscription matching and authorization use sub.Room).
		c := simulateClient(t, user, token, ident, subscription{Room: 1, Stream: "RoomChannel:1"})
		clients = append(clients, c)
		hub.clients[c] = struct{}{}
	}
	hub.mu.Unlock()

	hub.PublishStream(ctx, "RoomChannel:1", "hello")
	frames := make(map[string]*websocket.PreparedMessage, n)
	for _, c := range clients {
		select {
		case f := <-c.out:
			var got struct {
				Identifier string `json:"identifier"`
				Message    string `json:"message"`
			}
			if err := json.Unmarshal(f.Data(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Message != "hello" {
				t.Fatalf("wrong payload %q", got.Message)
			}
			key := got.Identifier
			if prev, dup := frames[key]; dup {
				if prev != f {
					t.Fatal("same identifier produced two frames in one publish")
				}
			} else {
				frames[key] = f
			}
		case <-time.After(10 * time.Second):
			t.Fatal("missing delivery")
		}
	}
	if len(frames) != n {
		t.Fatalf("delivered %d distinct identifiers, want %d", len(frames), n)
	}
	// The memo fallback frames are cached like the memo'd ones: republishing
	// the same payload returns the identical pointers.
	hub.PublishStream(ctx, "RoomChannel:1", "hello")
	for _, c := range clients {
		select {
		case f := <-c.out:
			var got struct {
				Identifier string `json:"identifier"`
			}
			if err := json.Unmarshal(f.Data(), &got); err != nil {
				t.Fatal(err)
			}
			if frames[got.Identifier] != f {
				t.Fatal("fallback frame not reused across publishes")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("missing delivery")
		}
	}
}

// TestPublishAllocsBounded pins the ENGINE-57 steady-state allocation
// contract after a warm-up publish (authorization cache, frame cache and
// scratch pool all populated): a stream broadcast allocates nothing at all,
// a room broadcast one small scope string.
func TestPublishAllocsBounded(t *testing.T) {
	hub, db, user, roomID, _ := hubFixture(t)
	ctx := context.Background()
	token, err := db.StartSession(ctx, user.ID, "allocs", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	roomIdent := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
	hub.mu.Lock()
	c := simulateClient(t, user, token, roomIdent, subscription{Channel: "RoomMessagesChannel", Room: roomID})
	hub.clients[c] = struct{}{}
	c2 := simulateClient(t, user, token, `{"channel":"Turbo::StreamsChannel","signed_stream_name":"r"}`, subscription{Stream: "rooms"})
	hub.clients[c2] = struct{}{}
	hub.mu.Unlock()

	markup := "<turbo-stream action=\"append\"><template>alloc-pin</template></turbo-stream>"
	hub.Publish(ctx, roomID, markup)        // prime authz + frame cache + pool
	hub.PublishStream(ctx, "rooms", markup) // prime the stream path
	if a := testing.AllocsPerRun(30, func() { hub.PublishStream(ctx, "rooms", markup) }); a != 0 {
		t.Fatalf("stream broadcast allocated %v allocs/op, want 0", a)
	}
	if a := testing.AllocsPerRun(30, func() { hub.Publish(ctx, roomID, markup) }); a != 1 {
		t.Fatalf("room broadcast allocated %v allocs/op, want 1 (scope string)", a)
	}
}

// TestConcurrentMixedPublishesDeliverExactPayloads hammers the pooled
// scratch, the shared frame cache and per-client queues from concurrent
// publishers: every client must receive exactly the published payload set,
// once each, with no duplicates or losses (pool poisoning or reuse races
// would corrupt frames or counts; -race exercises the pool handoff).
func TestConcurrentMixedPublishesDeliverExactPayloads(t *testing.T) {
	hub, db, user, roomID, _ := hubFixture(t)
	ctx := context.Background()
	token, err := db.StartSession(ctx, user.ID, "conc", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
	const (
		clients = 32
		posters = 4
		per     = 48
		total   = posters * per
	)
	hub.mu.Lock()
	var clientList []*client
	for i := 0; i < clients; i++ {
		c := simulateClient(t, user, token, identifier, subscription{Channel: "RoomMessagesChannel", Room: roomID})
		clientList = append(clientList, c)
		hub.clients[c] = struct{}{}
	}
	hub.mu.Unlock()

	errc := make(chan error, clients)
	var wg sync.WaitGroup
	for _, c := range clientList {
		wg.Add(1)
		go func(c *client) {
			defer wg.Done()
			counts := make(map[string]int, total)
			for seen := 0; seen < total; {
				select {
				case f := <-c.out:
					var fr struct {
						Identifier string `json:"identifier"`
						Message    string `json:"message"`
					}
					if err := json.Unmarshal(f.Data(), &fr); err != nil {
						errc <- fmt.Errorf("client %p undecodable frame %q: %v", c, f.Data(), err)
						return
					}
					if fr.Identifier != identifier {
						errc <- fmt.Errorf("client %p wrong identifier %q", c, fr.Identifier)
						return
					}
					if counts[fr.Message]++; counts[fr.Message] > 1 {
						errc <- fmt.Errorf("client %p duplicate payload %q", c, fr.Message)
						return
					}
					seen++
				case <-time.After(30 * time.Second):
					errc <- fmt.Errorf("client %p saw %d/%d frames", c, seen, total)
					return
				}
			}
		}(c)
	}
	for p := 0; p < posters; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				hub.Publish(ctx, roomID, fmt.Sprintf("<turbo-stream action=\"append\"><template>%d-%d</template></turbo-stream>", p, i))
			}
		}(p)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}
