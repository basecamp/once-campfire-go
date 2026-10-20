package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Existing reply tests cover direct delivery and no-loop publication. This case
// protects the distinct mentioned-outsider and already-admitted worker policies,
// plus SQL NULL versus raw stored HTML in the provider payload.
func TestWebhookPreparationKeepsMentionedAndAdmittedBots(t *testing.T) {
	app, _, _, owner := testApp(t)
	ctx := context.Background()
	type webhookPayload struct {
		Room    struct{ Path string }
		Message struct {
			Body struct {
				HTML  *string
				Plain string
			}
		}
	}
	received := make(chan webhookPayload, 2)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		received <- payload
		w.WriteHeader(204)
	}))
	defer hook.Close()
	endpoint := hook.URL
	bot, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Outside Bot", Role: 2, Webhook: &endpoint})
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Closed", nil, []int64{owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Room(ctx, bot.ID, room.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fixture bot belongs to room: %v", err)
	}
	token := app.Secrets.SGID(fmt.Sprintf("gid://campfire/User/%d?expires_in", bot.ID), "attachable", time.Time{})
	body := fmt.Sprintf(`<p>hello</p><action-text-attachment sgid="%s"></action-text-attachment><action-text-attachment sgid="%s"></action-text-attachment>`, token, token)
	result, err := app.MessageCommands.Create(ctx, owner.ID, room.ID, "", &body, nil)
	if err != nil {
		t.Fatal(err)
	}
	message := result.Commit.Message
	recipients, err := app.NotificationQueries.WebhookRecipients(ctx, app.presentationFacts(ctx), message, room)
	if err != nil || !slices.Equal(recipients, []int64{bot.ID}) {
		t.Fatalf("mentioned bot admission: %v %v", recipients, err)
	}
	direct := room
	direct.Type = "Rooms::Direct"
	recipients, err = app.NotificationQueries.WebhookRecipients(ctx, app.presentationFacts(ctx), message, direct)
	if err != nil || len(recipients) != 0 {
		t.Fatalf("direct admitted nonmember: %v %v", recipients, err)
	}
	// Delivery uses the admitted bot identity; it must not add a new role/status or
	// room-membership check at the worker boundary.
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET role=0,status=2 WHERE id=?", bot.ID); err != nil {
		t.Fatal(err)
	}
	recipients, err = app.NotificationQueries.WebhookRecipients(ctx, app.presentationFacts(ctx), message, room)
	if err != nil || len(recipients) != 0 {
		t.Fatalf("new admission accepted inactive non-bot: %v %v", recipients, err)
	}
	if err = app.WebhookReplies.Deliver(ctx, bot.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	payload := <-received
	if payload.Message.Body.HTML == nil || *payload.Message.Body.HTML != message.Body || payload.Room.Path != fmt.Sprintf("/rooms/%d/%s/messages", room.ID, bot.BotKey()) {
		t.Fatalf("admitted raw payload: %+v", payload)
	}
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE action_text_rich_texts SET body=NULL WHERE record_type='Message' AND record_id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	if err = app.WebhookReplies.Deliver(ctx, bot.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	payload = <-received
	if payload.Message.Body.HTML != nil || payload.Message.Body.Plain != "" {
		t.Fatalf("nullable payload: %+v", payload)
	}
}

// The query boundary must own badges and selected subscription credentials before
// queueing. Later membership changes must not rewrite that admitted selection.
func TestPushPreparationOwnsSelectedSubscriptionsAndBadges(t *testing.T) {
	app, _, _, owner := testApp(t)
	ctx := context.Background()
	member, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Recipient", Email: "recipient@test", Password: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil || len(rooms) == 0 {
		t.Fatal(rooms, err)
	}
	room := rooms[0]
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE memberships SET connected_at=NULL,involvement='everything',unread_at=? WHERE room_id=? AND user_id=?", database.Stamp(app.DB.Now()), room.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://example.test/one", "https://example.test/two"} {
		key, auth := "key", "auth"
		if err = app.DB.SavePushSubscription(ctx, member.ID, map[string]*string{"endpoint": &endpoint, "p256dh_key": &key, "auth_key": &auth}, "agent"); err != nil {
			t.Fatal(err)
		}
	}
	body := "<p>selected &amp; owned</p>"
	result, err := app.MessageCommands.Create(ctx, owner.ID, room.ID, "", &body, nil)
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := app.NotificationQueries.Push(ctx, app.presentationFacts(ctx), result.Commit.Message, room)
	if err != nil || len(deliveries) != 2 {
		t.Fatal(deliveries, err)
	}
	badge, err := app.DB.UnreadCount(ctx, member.ID)
	if err != nil || badge == 0 {
		t.Fatal(badge, err)
	}
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE memberships SET connected_at=?,unread_at=NULL WHERE user_id=?", database.Stamp(app.DB.Now()), member.ID); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		var payload struct {
			Title   string
			Options struct {
				Body string
				Data struct {
					Path  string
					Badge int64
				}
			}
		}
		if err = json.Unmarshal(delivery.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if delivery.Subscription.UserID != member.ID || delivery.Subscription.Key != "key" || delivery.Subscription.Auth != "auth" || payload.Title != room.Name || payload.Options.Body != owner.Name+": selected & owned" || payload.Options.Data.Path != fmt.Sprintf("/rooms/%d", room.ID) || payload.Options.Data.Badge != badge {
			t.Fatalf("selected delivery changed: %+v %+v", delivery, payload)
		}
	}
	fresh, err := app.NotificationQueries.Push(ctx, app.presentationFacts(ctx), result.Commit.Message, room)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("fresh connected admission: %v %v", fresh, err)
	}
}
