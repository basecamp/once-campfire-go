package web

import (
	"context"
	"github.com/basecamp/once-campfire-go/internal/database"
	"strings"
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
			base := database.Message{ID: -1, UpdatedAt: stamp}
			variants := []database.Message{
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
		if messageCacheKey(database.Message{ID: 1, UpdatedAt: pair[0]}) == messageCacheKey(database.Message{ID: 1, UpdatedAt: pair[1]}) {
			t.Fatal("out-of-range dates collided")
		}
	}
}

func TestMessageListKeyPreservesOrderAndBoundaries(t *testing.T) {
	a, b := database.Message{ID: 1}, database.Message{ID: 2}
	for _, pair := range [][2][]database.Message{
		{{a, b}, {b, a}},
		{{a}, {a, a}},
		{nil, {database.Message{}}},
	} {
		if messageListCacheKey(pair[0]) == messageListCacheKey(pair[1]) {
			t.Fatal("different ordered lists collided")
		}
	}
	if messageListCacheKey(nil) == messageCacheKey(database.Message{}) {
		t.Fatal("list and item namespaces collided")
	}
}

func TestMessageFragmentVersionAndBound(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "fragment", "<p>before</p>", "before")
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.messageItems(ctx, []database.Message{m})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first[0].Fragment), "before") {
		t.Fatal("missing rendered message")
	}
	// A cache hit skips rich text, boosts and attachment hydration entirely.
	cached, err := app.messageItems(ctx, []database.Message{m})
	if err != nil {
		t.Fatal(err)
	}
	if cached[0].Fragment != first[0].Fragment || cached[0].HTML != "" {
		t.Fatal("fragment was rebuilt")
	}
	m.UpdatedAt = m.UpdatedAt.Add(time.Microsecond)
	m.Body = "<p>after</p>"
	changed, err := app.messageItems(ctx, []database.Message{m})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changed[0].Fragment), "after") || changed[0].Fragment == first[0].Fragment {
		t.Fatal("new message version reused stale fragment")
	}
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

func TestStreamScrollBehavior(t *testing.T) {
	for _, test := range []struct {
		action, target string
		keep           bool
	}{
		{"append", "messages_rooms_open_1", false},
		{"remove", "message_uuid", false},
		{"replace", "message_uuid", false},
		{"replace", "presentation_message_uuid", true},
		{"append", "boosts_message_uuid", true},
		{"remove", "boost_1", false},
	} {
		actual := strings.Contains(stream(test.action, test.target, "content"), `maintain_scroll="true"`)
		if actual != test.keep {
			t.Fatalf("%s %s: keep scroll=%v", test.action, test.target, actual)
		}
	}
}

func TestMissingMessageAuthorKeepsPlaceholder(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "orphan", "hello", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec("UPDATE messages SET creator_id=999999 WHERE id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	messages, err := app.DB.Messages(ctx, rooms[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("orphan disappeared: %d messages", len(messages))
	}
	views, err := app.messageItems(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	if views[0].Fragment != unrenderableMessage {
		t.Fatal("missing author did not produce placeholder", views[0])
	}
}
