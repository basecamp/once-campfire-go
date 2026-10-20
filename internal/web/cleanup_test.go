package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestBanPurgesMessagesAndUnsharedFiles(t *testing.T) {
	app, _, _, owner := testApp(t)
	ctx := context.Background()
	user, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Banned", Email: "banned@test", Password: "digest", Bio: "", Role: 0, Webhook: nil})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	exclusive, err := app.Storage.Stage(ctx, "exclusive.txt", "text/plain", strings.NewReader("exclusive"))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := app.Storage.Stage(ctx, "shared.txt", "text/plain", strings.NewReader("shared"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ user, blob int64 }{{user.ID, exclusive.ID}, {user.ID, shared.ID}, {owner.ID, shared.ID}} {
		if _, err = app.DB.CreateMessage(ctx, entry.user, rooms[0].ID, database.MessageInput{Attachment: &entry.blob}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = app.DB.StartSession(ctx, user.ID, "browser", "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.AccountCommands.Ban(ctx, owner.ID, user.ID, true); err != nil {
		t.Fatal(err)
	}
	app.Jobs.Close(5 * time.Second)
	messages, err := messageRecords(app.DB, ctx, rooms[0].ID, 0)
	if err != nil || len(messages) != 1 || messages[0].CreatorID != owner.ID {
		t.Fatalf("remaining messages: %v %v", messages, err)
	}
	for _, blob := range []struct {
		id     int64
		key    string
		exists bool
	}{{exclusive.ID, exclusive.Key, false}, {shared.ID, shared.Key, true}} {
		path, _ := app.Storage.Path(blob.key)
		_, err = os.Stat(path)
		if (err == nil) != blob.exists {
			t.Errorf("file %s exists=%v: %v", path, blob.exists, err)
		}
	}
	var count int
	if err = app.DB.Read.QueryRow("SELECT count(*) FROM message_search_index").Scan(&count); err != nil || count != 1 {
		t.Fatalf("search index: %d %v", count, err)
	}
}
