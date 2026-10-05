package web

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func messageTemplate(t *testing.T, app *Server, view messageView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := app.templates.ExecuteTemplate(&buf, "message-uncached", view); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestPlainMessageMatchesTemplate(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil || len(rooms) != 1 {
		t.Fatal(rooms, err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, `cli"ent`, `<p>one &amp; two &lt; "x" 'y'</p>`, `one & two < "x" 'y'`)
	if err != nil {
		t.Fatal(err)
	}
	views, err := app.messageViews(ctx, []database.Message{message})
	if err != nil || len(views) != 1 || views[0].Fragment == "" {
		t.Fatal(views, err)
	}
	if got, want := string(views[0].Fragment), messageTemplate(t, app, views[0]); got != want {
		t.Fatalf("plain message diverged\n got: %s\nwant: %s", got, want)
	}

	emoji := views[0]
	emoji.AllEmoji = true
	emoji.Fragment = ""
	if got, want := app.renderPlainMessage(emoji), messageTemplate(t, app, emoji); got != want {
		t.Fatalf("emoji class diverged\n got: %s\nwant: %s", got, want)
	}

	quoted := views[0]
	quoted.Fragment = ""
	quoted.Creator = `A & B < "C"`
	quoted.CreatorTitle = `T & T`
	quoted.RoomName = `R <oom>`
	quoted.Permalink = `http://example.org/rooms/1?x=1&y=2`
	quoted.HTML = `<p>keep &amp; this</p>`
	quoted.CreatedAt = time.Date(2026, 3, 1, 2, 3, 4, 123000000, time.UTC)
	quoted.UpdatedAt = quoted.CreatedAt
	if got, want := app.renderPlainMessage(quoted), messageTemplate(t, app, quoted); got != want {
		t.Fatalf("escaped fields diverged\n got: %s\nwant: %s", got, want)
	}

	boosted, err := app.DB.CreateBoost(ctx, user.ID, message.ID, "🔥")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := app.DB.ReachableMessage(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	withBoost, err := app.messageViews(ctx, []database.Message{reloaded})
	if err != nil || len(withBoost[0].Boosts) != 1 || withBoost[0].Boosts[0].ID != boosted.ID {
		t.Fatal(withBoost, err)
	}
	if got, want := string(withBoost[0].Fragment), messageTemplate(t, app, withBoost[0]); got != want {
		t.Fatalf("boosted message should use the template\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(app.renderPlainMessage(withBoost[0]), "boost_") && plainMessage(withBoost[0]) {
		t.Fatal("a boosted message took the plain renderer")
	}

	attached := views[0]
	attached.Fragment = ""
	attached.Attachment = &storage.Blob{}
	if plainMessage(attached) {
		t.Fatal("an attachment must leave the fast path")
	}
}
