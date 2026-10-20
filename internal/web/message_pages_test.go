package web

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

func TestMessageFormsPreserveFullHydrationBytes(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	setFragmentLimit(app, 0)
	for _, body := range []string{`<p>Words &amp; <b>markup</b></p>`, `<action-text-attachment sgid="invalid"></action-text-attachment>`, `🎉`} {
		m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.DB.CreateBoost(ctx, user.ID, m.ID, `quoted" & boost`); err != nil {
			t.Fatal(err)
		}
		types := []string{""}
		if strings.Contains(body, "Words") {
			types = append(types, "text/plain", "image/png", "missing-creator")
		}
		for _, contentType := range types {
			if contentType == "missing-creator" {
				contentType = "text/plain"
				m.CreatorID = -1
				if _, err := app.DB.Write.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
					t.Fatal(err)
				}
				if _, err := app.DB.Write.ExecContext(ctx, "UPDATE messages SET creator_id=? WHERE id=?", m.CreatorID, m.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := app.DB.Write.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
			}
			if contentType != "" {
				blob, err := app.DB.CreateBlob(ctx, database.Blob{Filename: "file & name", ContentType: &contentType, ByteSize: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := app.DB.UpdateMessage(ctx, user.ID, m.ID, database.MessageInput{Attachment: &blob.ID}); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"edit-message", "new-boost", "boosts-index"} {
				before, err := app.MessageQueries.Views(ctx, app.presentationFacts(ctx), []database.Message{m.Message})
				if err != nil {
					t.Fatal(err)
				}
				if name == "edit-message" {
					doc := richtext.Prepare(m.Body)
					resolved, err := app.ContentQueries.Resolve(ctx, app.presentationFacts(ctx), doc.EditorAttachables())
					if err != nil {
						t.Fatal(err)
					}
					before[0].Editable, _ = doc.Editable(resolved)
				}
				var view presentation.MessageView
				switch name {
				case "edit-message":
					view, err = app.MessageQueries.Edit(ctx, app.presentationFacts(ctx), m.Message)
				case "boosts-index":
					view, err = app.MessageQueries.Boosts(ctx, m.Message)
				case "new-boost":
					view = presentation.ViewMessage(m.Message)
				}
				after := []presentation.MessageView{view}
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				var expected, actual bytes.Buffer
				p := page{User: user, Origin: "https://forms.test", Messages: before}
				if err := app.Presentation.ExecuteTemplate(&expected, name, p); err != nil {
					t.Fatal(err)
				}
				p.Messages = after
				if err := app.Presentation.ExecuteTemplate(&actual, name, p); err != nil {
					t.Fatal(err)
				}
				if actual.String() != expected.String() {
					t.Fatalf("%s/%s: focused form bytes changed", name, contentType)
				}
				if after[0].Fragment != "" {
					t.Fatal("form retained an unused message fragment")
				}
			}
			// Remove the attachment before testing the next type.
			if _, err := app.DB.Write.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type='Message' AND record_id=?", m.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMessageFormsKeepFreshBoostsAndAuthorization(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "<p>form body</p>"))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{fmt.Sprintf("/rooms/%d/messages/%d/edit", m.RoomID, m.ID), fmt.Sprintf("/messages/%d/boosts/new", m.ID), fmt.Sprintf("/messages/%d/boosts", m.ID)}
	get := func(path string) string {
		t.Helper()
		res, body := perform(t, server, "GET", path, "", nil, cookie)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %s", path, res.Status)
		}
		return string(body)
	}
	for _, path := range paths {
		get(path)
	}
	boost, err := app.DB.CreateBoost(ctx, user.ID, m.ID, "fresh form boost")
	if err != nil || !strings.Contains(get(paths[2]), "fresh form boost") {
		t.Fatalf("boost addition stale: %v", err)
	}
	if _, err := app.DB.DeleteBoost(ctx, user.ID, m.ID, boost.ID); err != nil || strings.Contains(get(paths[2]), "fresh form boost") {
		t.Fatalf("boost deletion stale: %v", err)
	}
	if _, err := app.DB.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id=?", user.ID, m.RoomID); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		res, _ := perform(t, server, "GET", path, "", nil, cookie)
		if res.StatusCode != 404 {
			t.Fatalf("%s bypassed revoked room membership: %s", path, res.Status)
		}
	}
}
