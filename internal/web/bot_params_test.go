package web

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestBotBareAttachmentKeepsRawBodyWithRealBotKey(t *testing.T) {
	app, server, _, owner := testApp(t)
	ctx := context.Background()
	bot, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Parameter bot", Role: 2})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(ctx, bot.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatalf("bot rooms: %v", err)
	}
	path := fmt.Sprintf("/rooms/%d/%s/messages", rooms[0].ID, bot.BotKey())
	for _, test := range []struct{ name, query, media, raw, want string }{
		{"bare query", "?attachment", "text/plain", "attachment bareneedle", "attachment bareneedle"},
		{"empty query", "?attachment=", "text/plain", "attachment bareneedle", ""},
		{"bare form", "", "application/x-www-form-urlencoded", "attachment", "attachment"},
		{"empty form", "", "application/x-www-form-urlencoded", "attachment=", ""},
		{"last query null", "?attachment=invalid&attachment", "text/plain", "attachment bareneedle", "attachment bareneedle"},
		{"last query empty", "?attachment&attachment=", "text/plain", "attachment bareneedle", ""},
		{"query null beats form", "?attachment", "application/x-www-form-urlencoded", "attachment=invalid", "attachment=invalid"},
		{"query empty beats form null", "?attachment=", "application/x-www-form-urlencoded", "attachment", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, _ := perform(t, server, "POST", path+test.query, test.media, strings.NewReader(test.raw), nil)
			if response.StatusCode != 201 {
				t.Fatalf("status %d, want 201", response.StatusCode)
			}
			location := response.Header.Get("Location")
			id, err := strconv.ParseInt(location[strings.LastIndexByte(location, '/')+1:], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			message, err := app.DB.Message(ctx, id)
			if err != nil || message.CreatorID != bot.ID || message.Body != test.want {
				t.Errorf("stored creator=%d body=%q, want creator=%d body=%q: %v", message.CreatorID, message.Body, bot.ID, test.want, err)
			}
			refs, err := app.DB.SearchReferences(ctx, bot.ID, "attachment")
			if err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, ref := range refs {
				matched = matched || ref.ID == id
			}
			if matched != strings.Contains(test.want, "attachment") {
				t.Errorf("FTS membership %v for stored body %q", matched, test.want)
			}
		})
	}
}
