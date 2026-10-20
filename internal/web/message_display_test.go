package web

import (
	"context"
	"fmt"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

// An unreadable mention record is not malformed submitted HTML. Rendering used
// to swallow this database failure inside the rich-text callback and return 200.
func TestMessageMentionReadFailureDoesNotRenderSuccess(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	target, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Mentioned", Email: "mentioned@test", Password: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`<action-text-attachment sgid="%s"></action-text-attachment>`, presentation.Mention(app.Secrets, database.UserDisplay{ID: target.ID}).SGID)
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, owner.ID, rooms[0].ID, messageInput("", body))
	if err != nil {
		t.Fatal(err)
	}
	// A real foreign-style malformed SQLite value makes the target's display
	// projection unreadable, while the author and message remain valid.
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET updated_at=? WHERE id=?", []byte("unreadable timestamp"), target.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		fmt.Sprintf("/rooms/%d/messages", rooms[0].ID),
		fmt.Sprintf("/rooms/%d/messages/%d/edit", rooms[0].ID, message.ID),
		fmt.Sprintf("/rooms/%d/api/messages", rooms[0].ID),
	} {
		t.Run(path, func(t *testing.T) {
			response, _ := perform(t, server, "GET", path, "", nil, cookie)
			if response.StatusCode != 500 {
				t.Fatalf("mention database failure hidden: %s", response.Status)
			}
		})
	}
}
