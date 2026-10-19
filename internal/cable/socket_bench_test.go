// ENGINE-54 socket-level fan-out bench: a real hub over loopback TCP with N
// real websocket clients (permessage-deflate, no context takeover, like the
// reference loadgen) all subscribed to the one room. Two workloads:
//
//   - capacitySample: open-loop sustained feed (four posters pushing
//     hub.Publish as fast as it goes — the in-app pacing element, the HTTP
//     post path's SQLite write, is deliberately absent so the socket layer
//     itself is what is measured). delivered/s at feed >> capacity is the
//     socket fan-out capacity, the same quantity bench/application's cable
//     row compares between Go and Rust.
//   - TestSocketFanoutAllDelivered: the always-on correctness companion, a
//     paced feed (rate safely under capacity) with strict zero-drop
//     accounting: every message to every subscriber.
//
// Gated behind CAMPFIRE_CABLE_SOCKET_BENCH=1 so the ordinary suite never
// pays for the capacity runs (subscribe + run at 1000 clients is seconds of
// wall clock). Run with the standard bench prefix, e.g.
//
//	taskset -c 16-31 nice -n 19 env GOMAXPROCS=4 \
//	  CAMPFIRE_CABLE_SOCKET_BENCH=1 go test -count=1 -run 'TestSocketFanoutSustained' \
//	  -timeout 280s -p 2 -tags sqlite_fts5 -v ./internal/cable/
package cable

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/coder/websocket"
)

func sockMarkup() string {
	return "<turbo-stream action=\"append\"><template>" + strings.Repeat("payload ", 50) + "</template></turbo-stream>"
}

// socketHub starts a real hub + HTTP server; every connection shares one
// session token (the loadgen's single login cookie).
func socketHub(t testing.TB) (*Hub, *database.DB, database.User, int64, *rails.Secrets, string) {
	t.Helper()
	hub, db, user, roomID, secrets := hubFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, err := db.StartSession(ctx, user.ID, "sockbench", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(w, r, user, token)
	}))
	t.Cleanup(server.Close)
	return hub, db, user, roomID, secrets, server.URL
}

// socketSubscriber dials one real client and waits through welcome and
// confirm_subscription for the room.
func socketSubscriber(t testing.TB, serverURL string, roomID int64, secrets *rails.Secrets) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	if err := wsjsonRead(ctx, conn, &welcome); err != nil || welcome["type"] != "welcome" {
		t.Fatalf("welcome: %v %v", welcome, err)
	}
	identifierBytes, err := json.Marshal(map[string]string{"channel": "RoomMessagesChannel", "signed_stream_name": secrets.SignStream(rails.RoomStream("Room", roomID))})
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjsonWrite(ctx, conn, map[string]string{"command": "subscribe", "identifier": string(identifierBytes)}); err != nil {
		t.Fatal(err)
	}
	for {
		var got map[string]any
		if err := wsjsonRead(ctx, conn, &got); err != nil {
			t.Fatal(err)
		}
		if got["type"] != "confirm_subscription" {
			continue
		}
		return conn
	}
}

func wsjsonRead(ctx context.Context, conn *websocket.Conn, v any) error {
	_, r, err := conn.Reader(ctx)
	if err != nil {
		return err
	}
	return json.NewDecoder(r).Decode(v)
}

func wsjsonWrite(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// socketClients subscribes n clients, each with a counting reader goroutine
// started immediately. The readers add one to counted per non-ping frame
// until they have counted target frames (target 0 = open-ended), readCtx is
// done or the connection errors.
func socketClients(t testing.TB, n int, target int, counted *atomic.Int64, serverURL string, roomID int64, secrets *rails.Secrets, readCtx context.Context) []*websocket.Conn {
	t.Helper()
	conns := make([]*websocket.Conn, n)
	for i := range conns {
		conns[i] = socketSubscriber(t, serverURL, roomID, secrets)
		go func(conn *websocket.Conn) {
			count := 0
			for {
				_, data, err := conn.Read(readCtx)
				if err != nil {
					return
				}
				if !bytes.Contains(data, []byte(`"type":"ping"`)) {
					count++
					if target > 0 {
						if count >= target {
							counted.Add(int64(count))
							return
						}
						continue
					}
					counted.Add(1)
				}
			}
		}(conns[i])
	}
	return conns
}

// socketInflight is the maximum number of posts not yet fully delivered; a
// watermark far below the 256-frame slow-client bound, so the pipe is kept
// saturated without ever overflowing a client queue (zero drops by
// construction).
const socketInflight = 48

// capacitySample feeds the hub with four poster goroutines bounded by the
// inflight watermark (posters wait while posted-received >= socketInflight),
// so the feed self-paces exactly at the socket layer's sustained capacity.
// The window excludes the pipeline fill and drain: posters run for window,
// then the in-flight tail (at most socketInflight posts) is let to drain
// before the readers are stopped. Returns the sustained post rate (which
// equals the delivery rate: postedN*clients frames land in the drain; a
// shortfall shows as fraction < 1) and the process-wide heap allocations
// per post (runtime.MemStats delta, all goroutines).
func capacitySample(t testing.TB, n int, window time.Duration, markup string) (postsPerSec, deliveredPerSec float64, fraction float64, allocsPerPost uint64) {
	t.Helper()
	hub, db, user, roomID, secrets, serverURL := socketHub(t)
	_ = db
	_ = user
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	readCtx, stopReads := context.WithCancel(context.Background())
	defer stopReads()
	var received atomic.Int64
	socketClients(t, n, 0, &received, serverURL, roomID, secrets, readCtx)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	end := time.Now().Add(window)
	var posted atomic.Int64
	const posters = 4
	var wg sync.WaitGroup
	start := time.Now()
	for p := 0; p < posters; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(end) {
				// A post counts as delivered once every client has its
				// frame (received/n), so in-flight posts stay bounded.
				if posted.Load()-received.Load()/int64(n) >= socketInflight {
					time.Sleep(20 * time.Microsecond)
					continue
				}
				hub.Publish(ctx, roomID, markup)
				posted.Add(1)
			}
		}()
	}
	wg.Wait()
	// Drain the in-flight tail (<= socketInflight posts) before counting.
	deadline := time.Now().Add(2 * time.Second)
	for posted.Load()*int64(n) > received.Load() && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
	stopReads()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	postedN := posted.Load()
	postsPerSec = float64(postedN) / elapsed.Seconds()
	deliveredPerSec = float64(received.Load()) / elapsed.Seconds()
	fraction = float64(received.Load()) / float64(postedN*int64(n))
	allocsPerPost = uint64(float64(after.TotalAlloc-before.TotalAlloc) / float64(postedN))
	return
}

// TestSocketFanoutSustained is the local stand-in for the application
// harness's cable row (bench/application --cable-clients). It reports the
// sustained delivered/s at 100 and 1000 clients with permessage-deflate.
func TestSocketFanoutSustained(t *testing.T) {
	if os.Getenv("CAMPFIRE_CABLE_SOCKET_BENCH") != "1" {
		t.Skip("set CAMPFIRE_CABLE_SOCKET_BENCH=1 to run the sustained socket fan-out bench")
	}
	markup := sockMarkup()
	for _, n := range []int{100, 1000} {
		window := 1500 * time.Millisecond
		if n == 1000 {
			window = 2000 * time.Millisecond
		}
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			posts, delivered, fraction, allocs := capacitySample(t, n, window, markup)
			t.Logf("clients=%d posts/s=%.0f delivered/s=%.0f (%.1f%% of feed) allocs/post=%d",
				n, posts, delivered, fraction*100, allocs)
		})
	}
}

// TestSocketFanoutSustainedLegacy runs the same workload with the
// CAMPFIRE_CABLE_FAST=off legacy one-write-per-frame path, reporting its
// delivered/s for wiring comparison (no assertion; the rate is reported).
func TestSocketFanoutSustainedLegacy(t *testing.T) {
	if os.Getenv("CAMPFIRE_CABLE_SOCKET_BENCH") != "1" {
		t.Skip("set CAMPFIRE_CABLE_SOCKET_BENCH=1 to run the sustained socket fan-out bench")
	}
	prev := os.Getenv("CAMPFIRE_CABLE_FAST")
	if err := os.Setenv("CAMPFIRE_CABLE_FAST", "off"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Setenv("CAMPFIRE_CABLE_FAST", prev) })
	markup := sockMarkup()
	for _, n := range []int{100, 1000} {
		window := 1500 * time.Millisecond
		if n == 1000 {
			window = 2000 * time.Millisecond
		}
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			posts, delivered, fraction, allocs := capacitySample(t, n, window, markup)
			t.Logf("legacy clients=%d posts/s=%.0f delivered/s=%.0f (%.1f%% of feed) allocs/post=%d",
				n, posts, delivered, fraction*100, allocs)
		})
	}
}

// TestSocketFanoutAllDelivered is the always-on correctness companion: a
// paced feed (rate safely under socket capacity) with strict per-client
// accounting and zero-drop assertion — every message to every subscriber
// over real sockets.
func TestSocketFanoutAllDelivered(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	hub, _, _, roomID, secrets, serverURL := socketHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const n = 100
	const posts = 200
	var received atomic.Int64
	// The readers know the exact expected count and stop when each client
	// has received all of them.
	socketClients(t, n, posts, &received, serverURL, roomID, secrets, ctx)

	markup := sockMarkup()
	interval := 700 * time.Microsecond // paces ~1,400 posts/s ≈ 140k delivered/s at 100 clients, well under capacity
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := p; i < posts; i += 4 {
				hub.Publish(ctx, roomID, markup)
				time.Sleep(interval)
			}
		}(p)
	}
	deadline := time.Now().Add(30 * time.Second)
	for received.Load() < n*posts && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if got := received.Load(); got != n*posts {
		t.Fatalf("delivered %d of %d", got, n*posts)
	}
	t.Logf("clients=100 posts=%d delivered=%d (zero-drop paced run)", posts, received.Load())
}
