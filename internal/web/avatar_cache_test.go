package web

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

// TestAvatarCachePinsValue pins the cache contract: hits return exactly the
// URL signedAvatar would produce for the same inputs, distinct (id, stamp)
// pairs never collide, and the zero-stamp (no "?v=") shape stays separate
// from every stamped shape.
func TestAvatarCachePinsValue(t *testing.T) {
	secrets, err := rails.NewSecrets("avatar-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	cache := newAvatarCache()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, id := range []int64{1, 2, 7, 1} {
		got := avatarURL(cache, secrets, id, stamp)
		want := signedAvatar(secrets, id, stamp)
		if got != want {
			t.Fatalf("entry %d: %q != %q", i, got, want)
		}
		if avatarURL(cache, secrets, id, stamp) != got {
			t.Fatalf("entry %d: cache hit changed value", i)
		}
	}
	// The unstamped shape (zero updated) is a distinct key.
	plain := avatarURL(cache, secrets, 1, time.Time{})
	if plain != signedAvatar(secrets, 1, time.Time{}) {
		t.Fatalf("unstamped: %q != %q", plain, signedAvatar(secrets, 1, time.Time{}))
	}
	if plain == avatarURL(cache, secrets, 1, stamp) {
		t.Fatal("stamped and unstamped avatar URLs must differ")
	}
	// A different updated time produces the different URL the signer gives,
	// and two updates in the same second share (the "?v=" stamp is second-
	// resolution).
	later := stamp.Add(60 * time.Second)
	if avatarURL(cache, secrets, 1, later) != signedAvatar(secrets, 1, later) {
		t.Fatal("stamped entry changed by time")
	}
	sameSecond := stamp.Add(100 * time.Millisecond)
	if avatarURL(cache, secrets, 1, sameSecond) != avatarURL(cache, secrets, 1, stamp) {
		t.Fatal("same-second updates must share the cached URL")
	}
	// Different secrets produce different URLs for the same inputs; the
	// cache is per-Server (per secrets), so a separate instance serves the
	// other key without cross-contamination.
	other, err := rails.NewSecrets("avatar-cache-other")
	if err != nil {
		t.Fatal(err)
	}
	if signedAvatar(other, 1, stamp) == signedAvatar(secrets, 1, stamp) {
		t.Fatal("different secrets must sign differently")
	}
	if got := avatarURL(newAvatarCache(), other, 1, stamp); got != signedAvatar(other, 1, stamp) {
		t.Fatal("second cache must sign under its own secrets")
	}
}

// TestAvatarCacheBounded pins the per-shard cap: a shard evicts past its
// bound instead of growing without limit, and an evicted entry re-signs
// correctly.
func TestAvatarCacheBounded(t *testing.T) {
	secrets, err := rails.NewSecrets("avatar-cache-bound")
	if err != nil {
		t.Fatal(err)
	}
	cache := newAvatarCache()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// Collect ids whose key lands in shard 0 so the eviction bound of one
	// shard is actually exercised.
	var ids []int64
	for id := int64(1); len(ids) < avatarCacheCap*2; id++ {
		k := avatarKey{id: id, v: uint64(stamp.UTC().Unix()) + 1}
		if cache.shard(k) == &cache.shards[0] {
			ids = append(ids, id)
		}
	}
	first, overfill := ids[:avatarCacheCap], ids[avatarCacheCap:avatarCacheCap+10]
	for _, id := range first {
		avatarURL(cache, secrets, id, stamp)
	}
	if n := len(cache.shards[0].m); n != avatarCacheCap {
		t.Fatalf("shard 0 holds %d entries after %d fills", n, avatarCacheCap)
	}
	for _, id := range overfill {
		avatarURL(cache, secrets, id, stamp)
	}
	if n := len(cache.shards[0].m); n > avatarCacheCap {
		t.Fatalf("shard 0 grew to %d entries", n)
	}
	// Evicted entries still resolve to the correct URL (a re-sign).
	for _, id := range first {
		if got := avatarURL(cache, secrets, id, stamp); got != signedAvatar(secrets, id, stamp) {
			t.Fatalf("id %d: %q", id, got)
		}
	}
}

// TestAvatarCacheConcurrent exercises the cache from many goroutines; the
// -race suite is the gate.
func TestAvatarCacheConcurrent(t *testing.T) {
	secrets, err := rails.NewSecrets("avatar-cache-race")
	if err != nil {
		t.Fatal(err)
	}
	cache := newAvatarCache()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := int64(g*50 + i%50)
				if got := avatarURL(cache, secrets, id, stamp); got != signedAvatar(secrets, id, stamp) {
					t.Errorf("goroutine %d id %d: %q", g, id, got)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkAvatarURL(b *testing.B) {
	secrets, err := rails.NewSecrets("avatar-cache-bench")
	if err != nil {
		b.Fatal(err)
	}
	cache := newAvatarCache()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var sink string
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink = avatarURL(cache, secrets, int64(i%8), stamp)
	}
	_ = fmt.Sprint(sink)
}
