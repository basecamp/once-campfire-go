package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestCommittedMessageNotifiesWhenPresentationFails(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	endpoint := webhook.URL
	bot, err := app.DB.CreateUser(ctx, user.ID, database.UserInput{Name: "Bot", Email: "", Password: "", Bio: "", Role: 2, Webhook: &endpoint})
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Direct", nil, []int64{bot.ID})
	if err != nil {
		t.Fatal(err)
	}
	// The write and webhook payload do not depend on boosts, but message display
	// does. Break that real read boundary rather than mocking the effects.
	if _, err = app.DB.Write.ExecContext(ctx, "DROP TABLE boosts"); err != nil {
		t.Fatal(err)
	}
	response, _ := perform(t, server, "POST", fmt.Sprintf("/rooms/%d/messages", room.ID), "application/x-www-form-urlencoded", strings.NewReader(url.Values{"message[body]": {"committedneedle"}}.Encode()), cookie)
	if response.StatusCode != 500 {
		t.Fatalf("expected rendering failure: %s", response.Status)
	}
	app.Jobs.Close(5 * time.Second)
	if calls.Load() != 1 {
		t.Fatalf("committed notification suppressed: %d deliveries", calls.Load())
	}
	messages, err := messageRecords(app.DB, ctx, room.ID, 0)
	if err != nil || len(messages) != 1 || messages[0].Body != "committedneedle" {
		t.Fatalf("committed message: %+v %v", messages, err)
	}
}

func TestBotWebhookReply(t *testing.T) {
	for _, attachment := range []bool{false, true} {
		t.Run(fmt.Sprintf("attachment=%t", attachment), func(t *testing.T) {
			app, server, cookie, user := testApp(t)
			ctx := context.Background()
			var calls atomic.Int32
			webhook := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					raw, _ := io.ReadAll(r.Body)
					var payload struct {
						User    struct{ ID int64 }
						Room    struct{ Path string }
						Message struct{ Body struct{ HTML, Plain string } }
					}
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Error(err)
					}
					if payload.User.ID != user.ID || payload.Message.Body.Plain != "hello" ||
						!strings.Contains(payload.Room.Path, "/messages") {
						t.Errorf("payload: %s", raw)
					}
					if attachment {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"reply":true}`)
					} else {
						w.Header().Set("Content-Type", "text/html")
						io.WriteString(w, "<p>Bot reply</p>")
					}
				}),
			)
			defer webhook.Close()
			endpoint := webhook.URL
			bot, err := app.DB.CreateUser(ctx, user.ID, database.UserInput{Name: "Reply Bot", Email: "", Password: "", Bio: "", Role: 2, Webhook: &endpoint})
			if err != nil {
				t.Fatal(err)
			}
			room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Direct", nil, []int64{bot.ID})
			if err != nil {
				t.Fatal(err)
			}
			response, data := perform(
				t,
				server,
				"POST",
				fmt.Sprintf("/rooms/%d/messages", room.ID),
				"application/x-www-form-urlencoded",
				strings.NewReader(url.Values{"message[body]": {"hello"}}.Encode()),
				cookie,
			)
			if response.StatusCode != 200 {
				t.Fatalf("post: %s %s", response.Status, data)
			}
			app.Jobs.Close(5 * time.Second)
			messages, err := messageRecords(app.DB, ctx, room.ID, 0)
			if err != nil || len(messages) != 2 {
				t.Fatalf("replies: %d %v", len(messages), err)
			}
			if messages[1].CreatorID != bot.ID || calls.Load() != 1 {
				t.Fatalf("reply creator/calls: %d %d", messages[1].CreatorID, calls.Load())
			}
			if attachment {
				blob, err := app.DB.AttachedBlob(ctx, "Message", messages[1].ID, "attachment")
				if err != nil || blob.Filename != "attachment.json" {
					t.Fatalf("attachment: %v %v", blob, err)
				}
			} else if messages[1].Body != "<p>Bot reply</p>" {
				t.Fatal(messages[1].Body)
			}
		})
	}
}
