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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// oldUnreadFanout is the pre-Lever-5 shape of the unread bump, kept for the
// benchmark A/B: read every room member, then one stream publish per member
// (each publish snapshots that member's stream, authorizes and sends). It
// iterates ROOM MEMBERS, not the server's unread subscribers, so it is
// unrelated-subscriber-independent too; the error is returned so a benchmark
// cannot silently measure a failed member read.
func oldUnreadFanout(hub *Hub, ctx context.Context, room int64) error {
	members, err := hub.db.RoomMemberIDs(ctx, room)
	if err != nil {
		return err
	}
	payload := map[string]any{"roomId": room}
	for _, id := range members {
		hub.PublishStream(ctx, fmt.Sprintf("user_%d_unreads", id), payload)
	}
	return nil
}

// BenchmarkUnreadBumpFanout compares the Lever-5 bump (a room-member read, one
// bulk authorization, one shared frame) with the shape it replaced, over a room
// with 100 members and 1 or 100 of them subscribed. Run with:
//
//	taskset -c 16-31 nice -n 19 env GOMAXPROCS=4 go test -run '^$' -bench BenchmarkUnreadBumpFanout -benchmem -count=5 ./internal/cable/
func BenchmarkUnreadBumpFanout(b *testing.B) {
	for _, subs := range []int{1, 100} {
		b.Run("subs="+strconv.Itoa(subs), func(b *testing.B) {
			hub, db, owner, roomID, _ := hubFixture(b)
			ctx := context.Background()
			users := []database.User{owner}
			for i := 1; i < 100; i++ {
				u, err := db.CreateUser(ctx, fmt.Sprintf("Member%d", i), fmt.Sprintf("m%d@test", i), "digest", "", 0, nil)
				if err != nil {
					b.Fatal(err)
				}
				users = append(users, u)
			}
			clients := make([]*client, 0, subs)
			for i := 0; i < subs; i++ {
				c, _ := unreadSubscriber(b, hub, db, users[i])
				clients = append(clients, c)
			}
			drain := func() {
				for _, c := range clients {
					c.q.clear()
				}
			}
			// Warm the frame and authorization paths so the measurement is
			// steady state.
			if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
				b.Fatal(err)
			}
			if err := oldUnreadFanout(hub, ctx, roomID); err != nil {
				b.Fatal(err)
			}
			drain()
			b.Run("bump", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					drain()
					if err := hub.PublishUnreadBump(roomID, unreadBudget); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("member-fanout", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					drain()
					if err := oldUnreadFanout(hub, ctx, roomID); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkUnreadBumpUnrelatedSubscribers pins the candidate strategy's
// scaling: a fixed two-member target room, one subscribed member, and 0/100/
// 10,000 unread subscribers whose users are members of no room the post
// touches. The bump's cost must be flat in that last number — an unrelated
// subscriber contributes no token, no row and no lock-held work.
//
// The shape being compared against is oldUnreadFanout above, which iterated
// the ROOM'S MEMBERS, not the server's unread subscribers: it is unaffected by
// unrelated subscribers too, and it paid one publish per room member instead
// (12.4 KB/op and 816 allocs/op against the bump's 33.8 KB and 689 in
// BenchmarkUnreadBumpFanout's 100-member room). What this benchmark pins is
// that the new candidate read did not trade that for a dependence on the
// server's subscriber count, which the earlier global-scan revision did.
// Run with the same pinned-CPU command as BenchmarkUnreadBumpFanout.
func BenchmarkUnreadBumpUnrelatedSubscribers(b *testing.B) {
	for _, straysN := range []int{0, 100, 10000} {
		b.Run("strays="+strconv.Itoa(straysN), func(b *testing.B) {
			hub, db, owner, _, _ := hubFixture(b)
			ctx := context.Background()
			member, err := db.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
			if err != nil {
				b.Fatal(err)
			}
			pair, err := db.CreateRoom(ctx, owner.ID, "Rooms::Closed", "Pair", []int64{owner.ID, member.ID})
			if err != nil {
				b.Fatal(err)
			}
			strays := make([]*client, 0, straysN)
			for i := 0; i < straysN; i++ {
				strays = append(strays, strayUnreadSubscriber(hub, 1_000_000+int64(i), func() {}))
			}
			memberC, _ := unreadSubscriber(b, hub, db, member)
			hub.PublishUnreadBump(pair.ID, unreadBudget)
			memberC.q.clear()
			_ = strays // never recipients: only the member's queue can fill
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				memberC.q.clear()
				hub.PublishUnreadBump(pair.ID, unreadBudget)
			}
		})
	}
}

// BenchmarkUnreadBumpFanoutAB is the alternating-order A/B for the same room:
// both shapes run inside one loop, which shape runs first flips every
// iteration, and the recipient queues are drained before each measured
// operation so neither shape ever meets the other's frames. Each shape's
// per-iteration times are reported as minimum, median, mean and spread
// (max-min) rather than as one ns/op for the pair, which would hide exactly the
// drift the alternation exists to expose. Run with -benchtime=1000x -count=6
// for six independent samples of those metrics.
func BenchmarkUnreadBumpFanoutAB(b *testing.B) {
	for _, subs := range []int{1, 100} {
		b.Run("subs="+strconv.Itoa(subs), func(b *testing.B) {
			hub, db, owner, roomID, _ := hubFixture(b)
			ctx := context.Background()
			users := []database.User{owner}
			for i := 1; i < 100; i++ {
				u, err := db.CreateUser(ctx, fmt.Sprintf("Member%d", i), fmt.Sprintf("m%d@test", i), "digest", "", 0, nil)
				if err != nil {
					b.Fatal(err)
				}
				users = append(users, u)
			}
			clients := make([]*client, 0, subs)
			for i := 0; i < subs; i++ {
				c, _ := unreadSubscriber(b, hub, db, users[i])
				clients = append(clients, c)
			}
			drain := func() {
				for _, c := range clients {
					c.q.clear()
				}
			}
			bump := func() error { return hub.PublishUnreadBump(roomID, unreadBudget) }
			fanout := func() error { return oldUnreadFanout(hub, ctx, roomID) }
			if err := bump(); err != nil {
				b.Fatal(err)
			}
			if err := fanout(); err != nil {
				b.Fatal(err)
			}
			drain()
			measure := func(run func() error) int64 {
				drain()
				start := time.Now()
				if err := run(); err != nil {
					b.Fatal(err)
				}
				return time.Since(start).Nanoseconds()
			}
			bumpSamples := make([]int64, 0, b.N)
			fanoutSamples := make([]int64, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if i%2 == 0 {
					bumpSamples = append(bumpSamples, measure(bump))
					fanoutSamples = append(fanoutSamples, measure(fanout))
				} else {
					fanoutSamples = append(fanoutSamples, measure(fanout))
					bumpSamples = append(bumpSamples, measure(bump))
				}
			}
			b.StopTimer()
			reportSamples(b, "ns/op-bump", bumpSamples)
			reportSamples(b, "ns/op-member-fanout", fanoutSamples)
		})
	}
}

// reportSamples reports one shape's sample distribution: minimum, median, mean
// and spread (max-min), the minimum being the least contaminated by the host.
func reportSamples(b *testing.B, name string, samples []int64) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	slices.Sort(samples)
	var total int64
	for _, sample := range samples {
		total += sample
	}
	b.ReportMetric(float64(samples[0]), name+"-min")
	b.ReportMetric(float64(samples[len(samples)/2]), name+"-median")
	b.ReportMetric(float64(total)/float64(len(samples)), name+"-mean")
	b.ReportMetric(float64(samples[len(samples)-1]-samples[0]), name+"-spread")
}

// BenchmarkUnreadEligibilityReads isolates the two eligibility reads the bump
// runs after its candidate snapshot, so each one's byte cost is attributable:
// the session check over n live tokens and the membership recheck over n
// targets. Run with the same pinned-CPU command as the fanout benchmarks.
func BenchmarkUnreadEligibilityReads(b *testing.B) {
	hub, db, owner, roomID, _ := hubFixture(b)
	ctx := context.Background()
	ids := []int64{owner.ID}
	tokens := []string{}
	token, err := db.StartSession(ctx, owner.ID, "eligibility", "127.0.0.1")
	if err != nil {
		b.Fatal(err)
	}
	tokens = append(tokens, token)
	for i := 1; i < 100; i++ {
		u, err := db.CreateUser(ctx, fmt.Sprintf("Member%d", i), fmt.Sprintf("m%d@test", i), "digest", "", 0, nil)
		if err != nil {
			b.Fatal(err)
		}
		token, err := db.StartSession(ctx, u.ID, "eligibility", "127.0.0.1")
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, u.ID)
		tokens = append(tokens, token)
	}
	_ = hub
	for _, n := range []int{1, 100} {
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			b.Run("sessions", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := db.AuthorizedSessions(ctx, tokens[:n], 0); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("membership", func(b *testing.B) {
				// The pooled destination the bump uses: a slice the caller
				// owns, reused across calls.
				dst := make([]int64, 0, 64)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var err error
					if dst, err = db.RoomMembersAmong(ctx, dst[:0], roomID, ids[:n]); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func BenchmarkPublishFanout(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run("clients="+strconv.Itoa(n), func(b *testing.B) {
			hub, db, user, roomID, _ := hubFixture(b)
			ctx := context.Background()
			identifier := `{"channel":"RoomMessagesChannel","room_id":` + strconv.FormatInt(roomID, 10) + `}`
			sub := subscription{Channel: "RoomMessagesChannel", Room: roomID}
			var clients []*client
			for i := 0; i < n; i++ {
				// One real session per client: the authorization query and
				// the cache key are per (room, token).
				token, err := db.StartSession(ctx, user.ID, "bench", "127.0.0.1")
				if err != nil {
					b.Fatal(err)
				}
				c := simulateClient(b, user, token, identifier, sub)
				clients = append(clients, c)
				hub.register(c)
			}
			markup := "<turbo-stream action=\"append\"><template>" + strings.Repeat("payload ", 50) + "</template></turbo-stream>"
			// drain empties every client queue so sendFrame always takes the
			// send arm and the 256-frame slow-client bound is never hit.
			drain := func() {
				for _, c := range clients {
					c.q.clear()
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
