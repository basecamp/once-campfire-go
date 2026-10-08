package front

// End-to-end benchmarks for the ENGINE-62 public-listener shape: the
// production public chain served by the owned loop, with the recorded-replay
// lane on (the new path) and hidden (the same loop running the map path, the
// loop's own contribution measured in isolation). The header shape and client
// mirror the ENGINE-52 harness (single keep-alive connection, loadgen headers,
// server and client in one process) so the numbers sit next to
// BenchmarkFront(Avatar|StaticCSS|Up).

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastserve"
)

func benchLoopServer(b *testing.B, handler http.Handler) (*fastserve.Server, *http.Client, string) {
	b.Helper()
	server := fastserve.New(handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go server.Serve(listener)
	b.Cleanup(func() { server.Close() })
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1}, Timeout: 10 * time.Second}
	b.Cleanup(client.CloseIdleConnections)
	return server, client, "http://" + listener.Addr().String()
}

func benchLoopRoute(b *testing.B, handler http.Handler, path, wantCache string) {
	_, client, url := benchLoopServer(b, handler)
	length := warmCache(b, client, url, path, wantCache)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := benchGet(b, client, url, path, 0)
		if len(body) != length {
			b.Fatalf("%s wire body %d bytes, want %d", path, len(body), length)
		}
	}
}

// BenchmarkFrontLoopUp measures /up through the public chain on the owned
// loop with the fixed table and the recorded lane active.
func BenchmarkFrontLoopUp(b *testing.B) {
	benchLoopRoute(b, benchChain(false, true), "/up", "miss")
}

// BenchmarkFrontLoopAvatarHit measures the cached avatar through the loop's
// recorded lane.
func BenchmarkFrontLoopAvatarHit(b *testing.B) {
	benchLoopRoute(b, benchChain(false, true), benchAvatarPath, "hit")
}

// BenchmarkFrontLoopStaticCSSHit measures the cached CSS through the loop's
// recorded lane.
func BenchmarkFrontLoopStaticCSSHit(b *testing.B) {
	benchLoopRoute(b, benchChain(false, true), benchCSSPath, "hit")
}

// BenchmarkFrontLoopAvatarHitMap hides the recorded receiver so the loop
// serves the identical entry through the header-map path: the lane's
// contribution at the public listener.
func BenchmarkFrontLoopAvatarHitMap(b *testing.B) {
	benchLoopRoute(b, hideRecorded(benchChain(false, true)), benchAvatarPath, "hit")
}

// BenchmarkFrontLoopStaticCSSHitMap is the CSS map-path counterpart.
func BenchmarkFrontLoopStaticCSSHitMap(b *testing.B) {
	benchLoopRoute(b, hideRecorded(benchChain(false, true)), benchCSSPath, "hit")
}
