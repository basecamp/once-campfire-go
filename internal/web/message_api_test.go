package web

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

func TestBotAPISelectsActionIndependentlyOfKeyText(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	rooms, err := app.DB.Rooms(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d/boosts-key/messages", rooms[0].ID)
	response, _ := perform(t, server, "POST", path, "text/plain", strings.NewReader("Selected message action"), cookie)
	if response.StatusCode != 201 {
		t.Fatalf("key text selected a boost instead of a message: %s", response.Status)
	}
	response, body := perform(t, server, "GET", path, "", nil, cookie)
	var messages []presentation.APIMessage
	if err := json.Unmarshal(body, &messages); err != nil || response.StatusCode != 200 || len(messages) != 1 || messages[0].Body.Plain != "Selected message action" {
		t.Fatalf("message action: %s %s, %v", response.Status, body, err)
	}
}

func TestMessageAPIKeepsBodiesRolesAndStrictPagination(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0].ID
	bot, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "API Bot", Email: "", Role: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Mentioned-user content and creator API roles are distinct projections.
	mention := presentation.Mention(app.Secrets, database.UserDisplay{ID: bot.ID})
	base := app.DB.Now().UTC()
	var records []database.Message
	for i, seconds := range []int{0, 1, 1, 2} {
		app.DB.Now = func() time.Time { return base.Add(time.Duration(seconds) * time.Second) }
		body := "<p> </p>"
		creator := owner.ID
		if i == 1 {
			body = fmt.Sprintf(`<p>Hello <action-text-attachment sgid="%s"></action-text-attachment></p>`, mention.SGID)
		} else if i == 2 {
			body = "<p>Bot body</p>"
			creator = bot.ID
		} else if i == 3 {
			body = ""
		}
		commit, err := app.DB.CreateMessage(ctx, creator, room, messageInput("", body))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, commit.Message)
	}
	blob, err := app.DB.CreateBlob(ctx, database.Blob{Filename: "API/file?.txt", ByteSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.UpdateMessage(ctx, owner.ID, records[3].ID, database.MessageInput{Attachment: &blob.ID}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d/api/messages", room)
	response, body := perform(t, server, "GET", path, "", nil, cookie)
	var messages []presentation.APIMessage
	if err = json.Unmarshal(body, &messages); err != nil || response.StatusCode != 200 || len(messages) != 4 || response.Header.Get("X-Total-Count") != "4" {
		t.Fatalf("API page: %s %s %v", response.Status, body, err)
	}
	if messages[1].Creator.Role != "administrator" || messages[2].Creator.Role != "bot" || messages[2].Creator.Name != "API Bot" || messages[3].Body.Plain != "API-file-.txt" || !strings.Contains(messages[1].Body.Plain, "API Bot") {
		t.Fatalf("API bodies/roles: %+v", messages)
	}
	// Compare the preserved API content consumer, not the displayed HTML fragment.
	content, err := app.ContentQueries.Content(ctx, app.presentationFacts(ctx), records[0].Body)
	if err != nil || messages[0].Body.Plain != content.Plain || messages[0].Body.HTML != content.BodyHTML {
		t.Fatalf("blank body changed without an attachment: %+v %+v %v", messages[0].Body, content, err)
	}
	for _, test := range []struct {
		query  string
		want   []int64
		link   string
		status int
	}{
		{fmt.Sprintf("?before=%d", records[1].ID), []int64{records[0].ID}, "", 200},
		{fmt.Sprintf("?after=%d", records[1].ID), []int64{records[3].ID}, "", 200},
		// The page selector and Link policy intentionally have different precedence.
		{fmt.Sprintf("?before=%d&after=%d", records[1].ID, records[3].ID), []int64{records[0].ID}, fmt.Sprintf("after=%d", records[0].ID), 200},
		{fmt.Sprintf("?before=%d&before=%d", records[3].ID, records[1].ID), []int64{records[0].ID}, "", 200},
		{fmt.Sprintf("?after=%d&after=%d", records[0].ID, records[1].ID), []int64{records[3].ID}, "", 200},
		{fmt.Sprintf("?before=%d&after=%d&after=", records[1].ID, records[0].ID), []int64{records[0].ID}, "", 200},
		{fmt.Sprintf("?before=+0d%dtrailing&after=+", records[1].ID), []int64{records[0].ID}, "", 200},
		{"?before=0", nil, "", 404},
		{fmt.Sprintf("?before=invalid&after=%d", records[1].ID), nil, "", 404},
		{"?after=invalid", nil, "", 404},
	} {
		t.Run(test.query, func(t *testing.T) {
			for _, endpoint := range []string{path, fmt.Sprintf("/rooms/%d/messages", room)} {
				response, body := perform(t, server, "GET", endpoint+test.query, "", nil, cookie)
				if response.StatusCode != test.status {
					t.Errorf("%s: status %d, want %d", endpoint, response.StatusCode, test.status)
					continue
				}
				if test.status != 200 {
					continue
				}
				if endpoint != path {
					for _, record := range records {
						present := strings.Contains(string(body), fmt.Sprintf(`data-message-id="%d"`, record.ID))
						if present != slices.Contains(test.want, record.ID) {
							t.Errorf("HTML message %d present=%v, want IDs %v", record.ID, present, test.want)
						}
					}
					continue
				}
				var messages []presentation.APIMessage
				if err := json.Unmarshal(body, &messages); err != nil {
					t.Fatal(err)
				}
				var ids []int64
				for _, message := range messages {
					ids = append(ids, message.ID)
				}
				link := response.Header.Get("Link")
				if !slices.Equal(ids, test.want) || (test.link == "" && link != "") || (test.link != "" && !strings.Contains(link, test.link)) {
					t.Errorf("records %v link %q, want IDs %v link %q", ids, link, test.want, test.link)
				}
			}
		})
	}
}
