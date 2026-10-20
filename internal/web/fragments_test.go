package web

import (
	"context"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func TestMessageFragmentVersionAndBound(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("fragment", "<p>before</p>"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := app.MessageQueries.Page(ctx, app.messageScope(ctx), user.ID, rooms[0].ID, 0, "around", false)
	if err != nil {
		t.Fatal(err)
	}
	body := func(part responsebody.Part) string {
		t.Helper()
		var b strings.Builder
		if _, err := part.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if !strings.Contains(body(first), "before") {
		t.Fatal("missing rendered message")
	}
	cached, _, err := app.MessageQueries.Page(ctx, app.messageScope(ctx), user.ID, rooms[0].ID, 0, "around", false)
	if err != nil || body(cached) != body(first) || cached.Digest() != first.Digest() {
		t.Fatal("scoped fragment bytes changed", err)
	}
	if _, err := app.DB.UpdateMessage(ctx, user.ID, m.ID, messageInput("", "<p>after</p>")); err != nil {
		t.Fatal(err)
	}
	changed, _, err := app.MessageQueries.Page(ctx, app.messageScope(ctx), user.ID, rooms[0].ID, 0, "around", false)
	if err != nil || !strings.Contains(body(changed), "after") || changed.Digest() == first.Digest() {
		t.Fatal("new observation reused stale fragment", err)
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
		actual := strings.Contains(rails.TurboStream(test.action, test.target, "content"), `maintain_scroll="true"`)
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
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("orphan", "hello"))
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
	messages, err := messageRecords(app.DB, ctx, rooms[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("orphan disappeared: %d messages", len(messages))
	}
	views, err := app.MessageQueries.Views(ctx, app.presentationFacts(ctx), messages)
	if err != nil {
		t.Fatal(err)
	}
	if views[0].Fragment != presentation.UnrenderableMessage {
		t.Fatal("missing author did not produce placeholder", views[0])
	}
}
