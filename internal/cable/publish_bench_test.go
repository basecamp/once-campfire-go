// Publish-path micro-benchmark (ENGINE-40b): one hub.Publish to N distinct
// sessions (one per simulated client) with a warm authorization cache and
// drained client queues, so the measurement is the steady-state fan-out cost:
// recipient snapshot, per-recipient authorization, frame reuse, per-recipient
// channel delivery. Run with a fixed benchmark CPU set, e.g.
//
//	taskset -c 16-31 nice -n 19 env GOMAXPROCS=4 go test -run '^$' -bench BenchmarkPublishFanout -benchmem -count=5 ./internal/cable/
package cable

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func BenchmarkPublishFanout(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run("clients="+strconv.Itoa(n), func(b *testing.B) {
			hub, db, user, roomID, _ := hubFixture(b)
			ctx := context.Background()
			identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
			sub := subscription{Channel: "RoomMessagesChannel", Room: roomID}
			var clients []*client
			hub.mu.Lock()
			for i := 0; i < n; i++ {
				// One real session per client: the authorization query and
				// the cache key are per (room, token).
				token, err := db.StartSession(ctx, user.ID, "bench", "127.0.0.1")
				if err != nil {
					b.Fatal(err)
				}
				c := simulateClient(b, user, token, identifier, sub)
				clients = append(clients, c)
				hub.clients[c] = struct{}{}
			}
			hub.mu.Unlock()
			markup := "<turbo-stream action=\"append\"><template>" + strings.Repeat("payload ", 50) + "</template></turbo-stream>"
			// drain empties every client queue so sendFrame always takes the
			// send arm and the 256-frame slow-client bound is never hit.
			drain := func() {
				for _, c := range clients {
				empty:
					for {
						select {
						case <-c.out:
							continue
						default:
							break empty
						}
					}
				}
			}
			hub.Publish(ctx, roomID, markup)
			drain()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drain()
				hub.Publish(ctx, roomID, markup)
			}
		})
	}
}
