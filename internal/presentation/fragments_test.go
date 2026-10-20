package presentation

import (
	"github.com/basecamp/once-campfire-go/internal/database"
	"testing"
	"time"
)

func TestMessageVersionMatchesStampIdentity(t *testing.T) {
	for name, stamp := range map[string]time.Time{
		"zero":       {},
		"pre-epoch":  time.Unix(-1, 999999001),
		"current":    time.Date(2026, 10, 5, 12, 0, 0, 123456001, time.UTC),
		"far future": time.Date(300000, 1, 1, 0, 0, 0, 1, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			base := database.MessageReference{ID: -1, UpdatedAt: stamp}
			variants := []database.MessageReference{
				{ID: -1, UpdatedAt: stamp.In(time.FixedZone("offset", 19800))},
				{ID: -1, UpdatedAt: stamp.Add(998 * time.Nanosecond)},
				{ID: -1, UpdatedAt: stamp.Add(time.Microsecond)},
				{ID: 0, UpdatedAt: stamp},
			}
			for _, other := range variants {
				equal := base.ID == other.ID && database.Stamp(base.UpdatedAt) == database.Stamp(other.UpdatedAt)
				if (messageCacheKey(base) == messageCacheKey(other)) != equal {
					t.Fatalf("key identity differs from Stamp: %v / %v", base, other)
				}
			}
		})
	}
	// Integer nanosecond/microsecond timestamps wrap outside their ranges.
	for _, pair := range [][2]time.Time{
		{{}, time.Unix(0, time.Time{}.UnixNano())},
		{time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC), time.UnixMicro(time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro())},
	} {
		if messageCacheKey(database.MessageReference{ID: 1, UpdatedAt: pair[0]}) == messageCacheKey(database.MessageReference{ID: 1, UpdatedAt: pair[1]}) {
			t.Fatal("out-of-range dates collided")
		}
	}
}

func TestMessageListKeyPreservesOrderAndBoundaries(t *testing.T) {
	a, b := database.MessageReference{ID: 1}, database.MessageReference{ID: 2}
	for _, pair := range [][2][]database.MessageReference{
		{{a, b}, {b, a}},
		{{a}, {a, a}},
		{nil, {database.MessageReference{}}},
	} {
		if messageListCacheKey(pair[0]) == messageListCacheKey(pair[1]) {
			t.Fatal("different ordered lists collided")
		}
	}
	if messageListCacheKey(nil) == messageCacheKey(database.MessageReference{}) {
		t.Fatal("list and item namespaces collided")
	}
}

func TestFragmentBudgetAndFirstWinner(t *testing.T) {
	cache := newFragmentCache(2048)
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		cache.put(key, "value")
	}
	if cache.bytes > 2048 {
		t.Fatal("unbounded cache", cache.bytes)
	}
	cache.put("first", "original")
	cache.put("first", "replacement")
	if value, _ := cache.get("first"); value != "original" {
		t.Fatal("first fragment replaced")
	}
	disabled := newFragmentCache(0)
	disabled.put("x", "hello")
	if _, ok := disabled.get("x"); ok {
		t.Fatal("disabled cache retained entry")
	}
}
