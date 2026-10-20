package web

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestCapturedMessageCannotPopulateNewObservation(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("provenance", "<p>captured-old-content</p>"))
	if err != nil {
		t.Fatal(err)
	}
	// Two valid commits may share the released microsecond timestamp identity.
	// The foreign-aware generation, not that timestamp, distinguishes the content.
	app.DB.Now = func() time.Time { return captured.UpdatedAt }
	current, err := app.DB.UpdateMessage(ctx, user.ID, captured.ID, messageInput("", "<p>committed-current-content</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if !current.UpdatedAt.Equal(captured.UpdatedAt) {
		t.Fatal("fixture did not retain timestamp tie")
	}
	// Background publication can still hold an older committed receipt. Its
	// response may use that receipt, but it cannot admit it as a newer observation.
	if _, err := capturedMessagePart(ctx, app, []database.Message{captured.Message}); err != nil {
		t.Fatal(err)
	}
	part, count, err := app.MessageQueries.Page(ctx, app.messageScope(ctx), user.ID, rooms[0].ID, 0, "around", false)
	if err != nil || count != 1 {
		t.Fatal("current scoped query failed", count, err)
	}
	var body bytes.Buffer
	if _, err := part.WriteTo(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), "committed-current-content") || strings.Contains(body.String(), "captured-old-content") {
		t.Fatalf("captured receipt poisoned current scoped query: %s", body.String())
	}
}
