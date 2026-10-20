package presentation

import (
	"bytes"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"html/template"
	"testing"
)

func TestMessageListOwnershipAndAdmission(t *testing.T) {
	app, _ := shellTestFragments(t)
	scope := MessageScope{Generation: 1}
	refs := []database.MessageReference{{ID: 1, RoomID: 1}}
	fragments := []template.HTML{"<p>owned bytes</p>"}
	original := app.RecordMessages(scope, refs, fragments)
	body := func(part responsebody.Part) string {
		t.Helper()
		var b bytes.Buffer
		if _, err := part.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	want := body(original)
	key := scope.key(messageListCacheKey(refs))
	cost := len(key) + len(want) + 240
	for _, test := range []struct {
		name     string
		limit    int
		retained bool
	}{
		{"disabled", 0, false},
		{"oversized", (cost - 1) * 4, false},
		{"admitted", cost * 4, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			app.cache = newFragmentCache(test.limit)
			part := app.RecordMessages(scope, refs, fragments)
			if body(part) != want || part.Digest() != original.Digest() {
				t.Fatal("cache admission changed response")
			}
			entry, retained := app.cache.entry(key)
			if retained != test.retained {
				t.Fatalf("retained=%v, want %v", retained, test.retained)
			}
			if retained && (entry.html != "" || entry.bytes != cost || body(entry.part) != want) {
				t.Fatal("list payload must be retained and charged once")
			}
			// Eviction releases the cache's reference, not an outstanding response.
			for i := 0; i < 40; i++ {
				app.cache.putEntry(fragmentEntry{key: fmt.Sprint(i), part: responsebody.NewPart([]byte(want))})
			}
			if _, retained := app.cache.entry(key); retained || app.cache.bytes > test.limit {
				t.Fatal("eviction/admission bound was not enforced")
			}
			if body(part) != want || body(original) != want {
				t.Fatal("eviction changed outstanding response bytes")
			}
		})
	}
}
