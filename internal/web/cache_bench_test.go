package web

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// BenchmarkCacheKeys measures the per-request key path the whole-response and
// sidebar-page caches run on every lookup: the zero-allocation preflight
// (responseKeySize / sidebarPageKeySize) and the builder+hash, separately and
// together. Both build and hash inside the envelope (0 B/op, 0 allocs/op); a
// shape outside the envelope is rejected by the preflight before the builder
// runs (see TestResponseCacheKeyEnvelope). The "total" sub-benchmarks are the
// honest production figure — the preflight runs before the builder on the real
// path. Before this change the response key returned a string (2 allocs /
// 152 B per call on this shape) and the sidebar key went through fmt.Sprintf.
func BenchmarkCacheKeys(b *testing.B) {
	b.Run("response-build", func(b *testing.B) {
		info, r, round := responseKeyFixture()
		var sink [32]byte
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var buf [responseKeyBufSize]byte
			sink = sha256.Sum256(appendResponseCacheKey(buf[:0], info, r, round, 42))
		}
		_ = sink
	})
	b.Run("response-preflight", func(b *testing.B) {
		info, r, round := responseKeyFixture()
		var sink int
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			size, ok := responseKeySize(info, r, round, 42)
			if !ok {
				b.Fatal("fixture outside the envelope")
			}
			sink = size
		}
		_ = sink
	})
	b.Run("response-total", func(b *testing.B) {
		info, r, round := responseKeyFixture()
		var sink [32]byte
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, ok := responseKeySize(info, r, round, 42); !ok {
				b.Fatal("fixture outside the envelope")
			}
			var buf [responseKeyBufSize]byte
			sink = sha256.Sum256(appendResponseCacheKey(buf[:0], info, r, round, 42))
		}
		_ = sink
	})
	b.Run("sidebar-build", func(b *testing.B) {
		u := database.User{ID: 3, Name: "fixture", Role: 1}
		var sink [32]byte
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var buf [maxSidebarPageKeySize]byte
			sink = sha256.Sum256(appendSidebarPageKey(buf[:0], 7, 1, u, false))
		}
		_ = sink
	})
	b.Run("sidebar-total", func(b *testing.B) {
		u := database.User{ID: 3, Name: "fixture", Role: 1}
		var sink [32]byte
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, ok := sidebarPageKeySize(7, 1, u, false); !ok {
				b.Fatal("fixture outside the envelope")
			}
			var buf [maxSidebarPageKeySize]byte
			sink = sha256.Sum256(appendSidebarPageKey(buf[:0], 7, 1, u, false))
		}
		_ = sink
	})
}

// benchSearchApp builds a full application for the warm-search benchmarks,
// optionally with the whole-response cache disabled, and returns the seeded
// user. The frozen clock keeps the rendered bytes deterministic.
func benchSearchApp(b *testing.B, responseMB string) (*Server, *httptest.Server, string, database.User) {
	b.Helper()
	// The application logs at Info for every New; silence it so the benchmark
	// output is readable (the front benchmarks do the same).
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previous) })
	if responseMB != "" {
		b.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", responseMB)
	}
	// The checkpointer's PASSIVE checkpoint can move the observed generation
	// mid-benchmark and invalidate every gated cache, turning a warm run into a
	// miss; push the timed fallback past the benchmark and turn the WAL growth
	// ceiling off so neither schedules a checkpoint, keeping the measured path
	// the warm one.
	b.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	b.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "0")
	b.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := b.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("warm-search-bench")
	if err != nil {
		b.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(app.Close)
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		b.Fatal(err)
	}
	token, err := db.StartSession(context.Background(), user.ID, "test", "127.0.0.1")
	if err != nil {
		b.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		b.Fatal(err)
	}
	cookie := "session_token=" + rails.EscapeCookie(signed) + "; _campfire_session=bench"
	server := httptest.NewServer(front.Deflate(app))
	b.Cleanup(server.Close)
	return app, server, cookie, user
}

// benchGet runs one keep-alive GET with the given Accept-Encoding and returns
// the raw wire length (the client decodes nothing).
func benchGet(b *testing.B, client *http.Client, url, path, cookie, accept string) int {
	b.Helper()
	request, err := http.NewRequest("GET", url+path, nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Cookie", cookie)
	if accept != "" {
		request.Header.Set("Accept-Encoding", accept)
	}
	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	length, err := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err != nil {
		b.Fatal(err)
	}
	if response.StatusCode != 200 {
		b.Fatalf("%s status %d", path, response.StatusCode)
	}
	return int(length)
}

// benchWarmSearch measures one warm search request after proving engagement:
// the fill plus a warm request run before the timer, and the hit counter must
// move (engages) or must not (a deliberately non-engaging shape). A request
// that never touches the round cannot be timed as a "hit".
func benchWarmSearch(b *testing.B, responseMB, accept string, seed, engages bool) {
	app, server, cookie, user := benchSearchApp(b, responseMB)
	ctx := context.Background()
	if seed {
		rooms, err := app.DB.Rooms(ctx, user.ID)
		if err != nil || len(rooms) == 0 {
			b.Fatal("fixture has no rooms")
		}
		for i := 0; i < 40; i++ {
			if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("bench-%d", i), fmt.Sprintf("<p>benchmarkneedle %d</p>", i), fmt.Sprintf("benchmarkneedle %d", i)); err != nil {
				b.Fatal(err)
			}
		}
	}
	const path = "/searches?q=benchmarkneedle"
	client := benchWebClient(b, server)
	benchGet(b, client, server.URL, path, cookie, accept)
	hits0, _ := app.responses.counters()
	benchGet(b, client, server.URL, path, cookie, accept)
	hits1, misses1 := app.responses.counters()
	if engages && hits1 <= hits0 {
		b.Fatalf("warm %s request did not hit the response cache (hits %d -> %d, misses %d)", accept, hits0, hits1, misses1)
	}
	if !engages && hits1 != hits0 {
		b.Fatalf("non-engaging %s request moved the hit counter (%d -> %d)", accept, hits0, hits1)
	}
	b.ReportAllocs()
	b.ResetTimer()
	// ReportMetric must follow ResetTimer: ResetTimer clears user metrics.
	b.ReportMetric(float64(hits1-hits0), "warm_hits")
	for i := 0; i < b.N; i++ {
		benchGet(b, client, server.URL, path, cookie, accept)
	}
}

// BenchmarkWarmSearchHandler measures the app cost of a warm search for the
// four response shapes: a matching result (the recorded piece path, identity
// and gzip) and an empty result (the map path; identity is admitted after the
// validator finalization fix, a non-recorded gzip body is not admitted because
// the stored coding must be the one the key names). Each sub-benchmark proves
// its own engagement before timing.
func BenchmarkWarmSearchHandler(b *testing.B) {
	cases := []struct {
		name, accept  string
		seed, engages bool
	}{
		{"identity-seeded", "identity", true, true},
		{"gzip-seeded", "gzip", true, true},
		{"identity-empty", "identity", false, true},
		{"gzip-empty", "gzip", false, false},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) { benchWarmSearch(b, "", c.accept, c.seed, c.engages) })
	}
}

// BenchmarkWarmSearchAB is the interleaved enabled/disabled A/B for the same
// request: response-cache on (the round serves the stored wire bytes) against
// response-cache off (the search result cache serves, or the route renders).
// The benchmark's own engagement assertion fails a run that never hits.
func BenchmarkWarmSearchAB(b *testing.B) {
	for _, c := range []struct {
		name, accept string
		seed         bool
	}{
		{"identity-seeded", "identity", true},
		{"gzip-seeded", "gzip", true},
		{"identity-empty", "identity", false},
	} {
		b.Run(c.name+"-on", func(b *testing.B) { benchWarmSearch(b, "", c.accept, c.seed, true) })
		b.Run(c.name+"-off", func(b *testing.B) { benchWarmSearch(b, "0", c.accept, c.seed, false) })
	}
}

// BenchmarkWarmSearchMixed measures a mixed room/search warm sequence under
// real eviction pressure. The response cache runs with a 1 MiB budget and the
// corpus renders a large page per query, so the flood of distinct matching
// queries crosses the budget and evicts the target; the benchmark ASSERTS the
// eviction (the target misses after the flood, hit counter unmoved) before it
// times the alternating room/search sequence. The "off" sub-benchmark times
// the same sequence with the response cache disabled.
func BenchmarkWarmSearchMixed(b *testing.B) {
	b.Run("on", func(b *testing.B) { benchWarmSearchMixed(b, "1") })
	b.Run("off", func(b *testing.B) { benchWarmSearchMixed(b, "0") })
}

func benchWarmSearchMixed(b *testing.B, responseMB string) {
	app, server, cookie, user := benchSearchApp(b, responseMB)
	engages := responseMB != "0"
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		b.Fatal("fixture has no rooms")
	}
	room := rooms[0]
	words := make([]string, 0, 90)
	for i := 0; i < 90; i++ {
		words = append(words, fmt.Sprintf("floodword%03d", i))
	}
	corpus := strings.Join(words, " ")
	// Forty messages each carrying every flood word: every query matches all
	// forty, so one search page is large enough that ninety distinct queries
	// cross a 1 MiB budget.
	for i := 0; i < 40; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("mixed-%d", i), "<p>"+corpus+"</p>", corpus); err != nil {
			b.Fatal(err)
		}
	}
	client := benchWebClient(b, server)
	const target = "/searches?q=floodword000"
	roomPath := fmt.Sprintf("/rooms/%d", room.ID)
	benchGet(b, client, server.URL, target, cookie, "gzip")
	benchGet(b, client, server.URL, roomPath, cookie, "gzip")
	for i := 1; i < 90; i++ {
		benchGet(b, client, server.URL, fmt.Sprintf("/searches?q=floodword%03d", i), cookie, "gzip")
	}
	var entries, size, limit int
	var warmHits uint64
	if engages {
		entries, size, limit = app.responses.stats()
		if size > limit {
			b.Fatalf("cache over budget: size %d > limit %d", size, limit)
		}
		hits0, misses0 := app.responses.counters()
		benchGet(b, client, server.URL, target, cookie, "gzip")
		hits1, misses1 := app.responses.counters()
		if hits1 != hits0 || misses1 <= misses0 {
			b.Fatalf("the flood did not evict the target: hits %d->%d misses %d->%d (entries %d size %d limit %d)", hits0, hits1, misses0, misses1, entries, size, limit)
		}
		// The eviction probe re-filled the target; prove the timed sequence is
		// warm.
		hits2, _ := app.responses.counters()
		benchGet(b, client, server.URL, target, cookie, "gzip")
		hits3, misses3 := app.responses.counters()
		if hits3 <= hits2 {
			b.Fatalf("target did not hit after re-fill (hits %d -> %d, misses %d)", hits2, hits3, misses3)
		}
		warmHits = hits3 - hits2
	}
	b.ReportAllocs()
	b.ResetTimer()
	// ReportMetric must follow ResetTimer: ResetTimer clears user metrics.
	if engages {
		b.ReportMetric(float64(warmHits), "warm_hits")
		b.ReportMetric(float64(entries), "entries_after_flood")
		b.ReportMetric(float64(size), "size_after_flood")
		b.ReportMetric(float64(limit), "limit")
	}
	for i := 0; i < b.N; i++ {
		benchGet(b, client, server.URL, target, cookie, "gzip")
		benchGet(b, client, server.URL, roomPath, cookie, "gzip")
	}
}
