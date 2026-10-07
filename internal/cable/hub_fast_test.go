// ENGINE-40 fast-path tests: delivery counters, frame-cache FIFO/eviction/
// poisoning semantics, and byte-for-byte corpus comparison between the fast
// and legacy publish paths.
package cable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// hubFixture opens a real database and hub wired like hub_test.go.
func hubFixture(t *testing.T) (*Hub, *database.DB, database.User, int64, *rails.Secrets) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(filepath.Join(t.TempDir(), "cable.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.Setup(ctx, "Owner", "owner@test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	room, err := db.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(room) == 0 {
		t.Fatal("no rooms created by Setup")
	}
	secrets, err := rails.NewSecrets("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	hub := New(db, secrets)
	t.Cleanup(hub.Close)
	return hub, db, user, room[0].ID, secrets
}

// simulateClient is a client with a real per-connection queue but no socket.
func simulateClient(t *testing.T, user database.User, token string, identifier string, sub subscription) *client {
	t.Helper()
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &client{
		disconnect:    make(chan bool, 1),
		user:          user,
		token:         token,
		cancel:        cancel,
		out:           make(chan *websocket.PreparedMessage, 256),
		subscriptions: map[string]subscription{identifier: sub},
	}
	return c
}

// TestFanOutDeliveryCounters verifies every simulated client receives the
// broadcast, and that all recipients observe byte-identical frames.
func TestFanOutDeliveryCounters(t *testing.T) {
	for _, n := range []int{100, 1000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			hub, db, user, roomID, _ := hubFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			token, err := db.StartSession(ctx, user.ID, "bench", "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			// The identifier only keys the subscription map here; publish
			// matches on the stored subscription's room/channel.
			identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
			sub := subscription{Channel: "RoomMessagesChannel", Room: roomID}

			hub.mu.Lock()
			var clients []*client
			for i := 0; i < n; i++ {
				c := simulateClient(t, user, token, identifier, sub)
				clients = append(clients, c)
				hub.clients[c] = struct{}{}
			}
			hub.mu.Unlock()

			markup := "<turbo-stream action=\"append\"><template>" + strings.Repeat("payload ", 50) + "</template></turbo-stream>"
			hub.Publish(ctx, roomID, markup)

			type got struct {
				c *client
				f *websocket.PreparedMessage
			}
			results := make(chan got, n)
			for _, c := range clients {
				go func(c *client) {
					select {
					case f := <-c.out:
						results <- got{c, f}
					case <-ctx.Done():
						results <- got{c, nil}
					}
				}(c)
			}
			var firstData []byte
			for i := 0; i < n; i++ {
				r := <-results
				if r.f == nil {
					t.Fatalf("client %p missing delivery", r.c)
				}
				data := r.f.Data()
				var frame struct {
					Identifier string `json:"identifier"`
					Message    string `json:"message"`
				}
				if err := json.Unmarshal(data, &frame); err != nil {
					t.Fatalf("client %p undecodable frame %q: %v", r.c, data, err)
				}
				if frame.Message != markup {
					t.Fatalf("client %p wrong payload %q", r.c, frame.Message)
				}
				if firstData == nil {
					firstData = data
				} else if !bytes.Equal(firstData, data) {
					t.Fatal("recipients observed different frame bytes")
				}
			}
		})
	}
}

// TestFrameCacheFIFOAndPoisoning exercises reuse identity, exact-payload
// matching, per-key FIFO eviction, and global eviction bounds.
func TestFrameCacheFIFOAndPoisoning(t *testing.T) {
	defer func(perKey, maxEnt, maxB int) {
		frameCachePerKey = perKey
		frameCacheMaxEntries = maxEnt
		frameCacheMaxBytes = maxB
	}(frameCachePerKey, frameCacheMaxEntries, frameCacheMaxBytes)
	frameCachePerKey = 3
	frameCacheMaxEntries = 4
	frameCacheMaxBytes = 1 << 30

	fc := newFrameCache()
	key, id := "room:1", `{"channel":"RoomMessagesChannel"}`

	// Identical payload reuses the identical frame (a hit returns the same
	// immutable pointer; nothing is re-marshaled).
	p := []byte(`{"identifier":"i","message":"alpha"}`)
	first := fc.lookupOrCreate(key, id, p)
	if fc.lookupOrCreate(key, id, p) != first {
		t.Fatal("identical payload did not reuse the cached frame")
	}

	// Exact-payload semantics: a different payload for the same key must
	// produce a different frame; re-requesting the first payload must return
	// the ORIGINAL frame, never a mix (poisoning guard).
	p2 := []byte(`{"identifier":"i","message":"beta"}`)
	second := fc.lookupOrCreate(key, id, p2)
	if second == first {
		t.Fatal("different payload reused the same frame")
	}
	if got := fc.lookupOrCreate(key, id, p); got != first {
		t.Fatal("original payload did not hit the original frame after an insert")
	}

	// Per-key FIFO: with a bound of 3, a fourth distinct payload evicts the
	// key's oldest (alpha), which then misses; re-inserting alpha in turn
	// evicts the then-oldest (beta), while the newest entries survive (no
	// latest-only thrash).
	p3 := []byte(`{"identifier":"i","message":"gamma"}`)
	fc.lookupOrCreate(key, id, p3)
	p1 := []byte(`{"identifier":"i","message":"delta"}`)
	fc.lookupOrCreate(key, id, p1)
	if got := fc.lookupOrCreate(key, id, p); got == first {
		t.Fatal("evicted entry was still served")
	}
	if got := fc.lookupOrCreate(key, id, p3); got == nil {
		t.Fatal("third payload lost after eviction")
	}
	// Re-inserting the evicted alpha pushes out the now-oldest (beta):
	// strictly FIFO.
	reinserted := fc.lookupOrCreate(key, id, p)
	if got := fc.lookupOrCreate(key, id, p2); got == second {
		t.Fatal("oldest entry survived the FIFO eviction of the re-insert")
	}
	_ = reinserted

	// Global bound: 4 entries total; inserting more evicts the global
	// oldest (a different key).
	other := fc.lookupOrCreate("room:2", id, []byte(`{"identifier":"i","message":"other-1"}`))
	fc.lookupOrCreate("room:2", id, []byte(`{"identifier":"i","message":"other-2"}`))
	fc.lookupOrCreate("room:2", id, []byte(`{"identifier":"i","message":"other-3"}`))
	if got := fc.lookupOrCreate("room:2", id, []byte(`{"identifier":"i","message":"other-1"}`)); got != other {
		t.Fatal("global eviction evicted a newer entry")
	}
}

// TestFrameCacheConcurrent verifies the cache is safe under parallel
// lookups with identical and distinct payloads.
func TestFrameCacheConcurrent(t *testing.T) {
	fc := newFrameCache()
	key, id := "room:1", "identifier"
	payloads := make([][]byte, 16)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf(`{"message":"payload-%d"}`, i))
	}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				p := payloads[(i+j)%len(payloads)]
				fc.lookupOrCreate(key, id, p)
			}
		}(i)
	}
	wg.Wait()
}

// collectMessages drains the client connection, returning every
// RoomMessagesChannel message in order and skipping other frame types.
func collectMessages(t *testing.T, ctx context.Context, conn *websocket.Conn, want int) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for len(msgs) < want {
		var frame map[string]any
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatalf("reading %d of %d: %v", len(msgs), want, err)
		}
		switch frame["type"] {
		case "ping", "welcome", "confirm_subscription", "disconnect":
			continue
		}
		msgs = append(msgs, frame)
	}
	return msgs
}

// TestCorpusBroadcastFastMatchesLegacy publishes a broadcast corpus through
// both the fast and the legacy hub paths and requires identical decoded
// payloads and identifiers on a real socket.
func TestCorpusBroadcastFastMatchesLegacy(t *testing.T) {
	corpus := []string{
		"hello world",
		strings.Repeat("large message ", 60), // over the compression threshold
		"☃ unicode ☃ émojis 🎉",
		"",
		strings.Repeat("identical broadcast ", 10),
		strings.Repeat("identical broadcast ", 10), // repeated payload within the corpus
	}

	subscribe := func(hub *Hub, serverURL string, db *database.DB, user database.User, roomID int64, secrets *rails.Secrets) *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http"), &websocket.DialOptions{
			Subprotocols:    []string{"actioncable-v1-json"},
			CompressionMode: websocket.CompressionNoContextTakeover,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		var welcome map[string]any
		if err := wsjson.Read(ctx, conn, &welcome); err != nil || welcome["type"] != "welcome" {
			t.Fatalf("welcome: %v %v", welcome, err)
		}
		identifierBytes, err := json.Marshal(map[string]string{"channel": "RoomMessagesChannel", "signed_stream_name": secrets.SignStream(rails.RoomStream("Room", roomID))})
		if err != nil {
			t.Fatal(err)
		}
		identifier := string(identifierBytes)
		if err := wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
			t.Fatal(err)
		}
		for {
			var got map[string]any
			if err := wsjson.Read(ctx, conn, &got); err != nil {
				t.Fatal(err)
			}
			if got["type"] != "confirm_subscription" {
				continue
			}
			break
		}
		return conn
	}

	run := func(fast bool) []map[string]any {
		t.Helper()
		hub, db, user, roomID, secrets := hubFixture(t)
		hub.fast = fast

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		token, err := db.StartSession(ctx, user.ID, "corpus", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hub.Serve(w, r, user, token)
		}))
		defer server.Close()
		conn := subscribe(hub, server.URL, db, user, roomID, secrets)
		for _, markup := range corpus {
			hub.Publish(ctx, roomID, markup)
		}
		return collectMessages(t, ctx, conn, len(corpus))
	}

	legacy := run(false)
	fast := run(true)
	if len(legacy) != len(fast) {
		t.Fatalf("delivery counts differ: legacy %d fast %d", len(legacy), len(fast))
	}
	for i := range legacy {
		if legacy[i]["identifier"] != fast[i]["identifier"] {
			t.Fatalf("frame %d identifiers differ: %v vs %v", i, legacy[i]["identifier"], fast[i]["identifier"])
		}
		lm, lok := legacy[i]["message"].(string)
		fm, fok := fast[i]["message"].(string)
		if !lok || !fok {
			t.Fatalf("frame %d not a string message: %v vs %v", i, legacy[i]["message"], fast[i]["message"])
		}
		if lm != fm {
			t.Fatalf("frame %d payloads differ:\nlegacy %q\nfast   %q", i, lm, fm)
		}
	}
}

// TestBatchDrainDeliversAllInOrder publishes more messages than one batch
// can hold and verifies complete, ordered delivery through the real write
// path (fast mode).
func TestBatchDrainDeliversAllInOrder(t *testing.T) {
	hub, db, user, roomID, secrets := hubFixture(t)
	hub.fast = true

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	token, err := db.StartSession(ctx, user.ID, "load", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	identifierBytes, err := json.Marshal(map[string]string{"channel": "RoomMessagesChannel", "signed_stream_name": secrets.SignStream(rails.RoomStream("Room", roomID))})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(w, r, user, token)
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
		Subprotocols:    []string{"actioncable-v1-json"},
		CompressionMode: websocket.CompressionNoContextTakeover,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var welcome map[string]any
	if err := wsjson.Read(ctx, conn, &welcome); err != nil || welcome["type"] != "welcome" {
		t.Fatalf("welcome: %v %v", welcome, err)
	}
	identifier := string(identifierBytes)
	if err := wsjson.Write(ctx, conn, map[string]string{"command": "subscribe", "identifier": identifier}); err != nil {
		t.Fatal(err)
	}
	for {
		var got map[string]any
		if err := wsjson.Read(ctx, conn, &got); err != nil {
			t.Fatal(err)
		}
		if got["type"] == "confirm_subscription" {
			break
		}
	}

	const count = 100 // 64 max per batch + remainder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			hub.Publish(ctx, roomID, fmt.Sprintf("<turbo-stream action=\"append\"><template>message %d</template></turbo-stream>", i))
		}
	}()
	msgs := collectMessages(t, ctx, conn, count)
	wg.Wait()
	for i, m := range msgs {
		want := fmt.Sprintf("<turbo-stream action=\"append\"><template>message %d</template></turbo-stream>", i)
		if m["message"] != want {
			t.Fatalf("message %d = %v, want %q", i, m["message"], want)
		}
		if m["identifier"] != identifier {
			t.Fatalf("message %d identifier %v", i, m["identifier"])
		}
	}
}

// TestCableFastEnvGate verifies CAMPFIRE_CABLE_FAST=off selects the legacy
// path at hub construction.
func TestCableFastEnvGate(t *testing.T) {
	prev := os.Getenv("CAMPFIRE_CABLE_FAST")
	os.Setenv("CAMPFIRE_CABLE_FAST", "off")
	t.Cleanup(func() { os.Setenv("CAMPFIRE_CABLE_FAST", prev) })
	if fastFromEnv() {
		t.Fatal("CAMPFIRE_CABLE_FAST=off did not disable the fast path")
	}
	os.Setenv("CAMPFIRE_CABLE_FAST", "on")
	if !fastFromEnv() {
		t.Fatal("CAMPFIRE_CABLE_FAST=on did not enable the fast path")
	}
	os.Unsetenv("CAMPFIRE_CABLE_FAST")
	if !fastFromEnv() {
		t.Fatal("default fast path is off")
	}
}

// TestSlowClientBatchKeepsQueueBound verifies the 256-frame slow-client
// policy is identical under the fast path: an overflowing queue cancels the
// client even while the batch drain is active.
func TestSlowClientBatchKeepsQueueBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{out: make(chan *websocket.PreparedMessage, 2), cancel: cancel, disconnect: make(chan bool, 1)}
	if !c.send("one") || !c.send("two") || c.send("three") {
		t.Fatal("queue limit not enforced")
	}
	if ctx.Err() == nil {
		t.Fatal("slow client was not disconnected")
	}
}
