package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

// NotificationQueries prepares owned deliveries at the existing selection points:
// push recipients/badges before admission, webhook payloads in admitted workers.
type NotificationQueries struct {
	DB      *database.DB
	Content *ContentQueries
}

type PushDelivery struct {
	Subscription database.PushSubscription
	Payload      []byte
}

func (s *NotificationQueries) TestPush(ctx context.Context, user, subscription int64, title, body, path string) (PushDelivery, error) {
	sub, err := s.DB.PushSubscription(ctx, user, subscription)
	if err != nil {
		return PushDelivery{}, err
	}
	badge, err := s.DB.UnreadCount(ctx, user)
	if err != nil {
		return PushDelivery{}, err
	}
	return PushDelivery{Subscription: sub, Payload: integrations.NotificationJSON(title, body, path, badge)}, nil
}

func (s *NotificationQueries) Push(ctx context.Context, facts presentation.Facts, message database.Message, room database.Room) ([]PushDelivery, error) {
	mentions, err := s.Content.MentionedIDs(ctx, facts, message.Body)
	if err != nil {
		return nil, fmt.Errorf("push mentions: %w", err)
	}
	subscriptions, err := s.DB.PushRecipients(ctx, room.ID, message.CreatorID, mentions)
	if err != nil {
		return nil, fmt.Errorf("push recipients: %w", err)
	}
	if len(subscriptions) == 0 {
		return nil, nil
	}
	body, err := s.Content.PlainText(ctx, facts, message.Body)
	if err != nil {
		return nil, fmt.Errorf("push body: %w", err)
	}
	if strings.TrimSpace(body) == "" {
		if attachment, err := s.DB.AttachedBlob(ctx, "Message", message.ID, "attachment"); err == nil && attachment.ID != 0 {
			body = attachment.Filename
		}
	}
	title := room.Name
	if room.Type == "Rooms::Direct" {
		title = message.Creator
	} else {
		body = message.Creator + ": " + body
	}
	title = integrations.TruncatePush(title, 256)
	body = integrations.TruncatePush(body, 3072)
	ids := make([]int64, 0, len(subscriptions))
	for _, sub := range subscriptions {
		ids = append(ids, sub.UserID)
	}
	badges, err := s.DB.UnreadCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("push badges: %w", err)
	}
	deliveries := make([]PushDelivery, 0, len(subscriptions))
	for _, sub := range subscriptions {
		deliveries = append(deliveries, PushDelivery{Subscription: sub, Payload: integrations.NotificationJSON(title, body, fmt.Sprintf("/rooms/%d", room.ID), badges[sub.UserID])})
	}
	return deliveries, nil
}

func (s *NotificationQueries) WebhookRecipients(ctx context.Context, facts presentation.Facts, message database.Message, room database.Room) ([]int64, error) {
	if room.Type == "Rooms::Direct" {
		return s.DB.DirectWebhookRecipients(ctx, room.ID, message.CreatorID)
	}
	ids, err := s.Content.MentionedIDs(ctx, facts, message.Body)
	if err != nil {
		return nil, err
	}
	return s.DB.MentionedWebhookRecipients(ctx, ids, message.CreatorID)
}

type WebhookDelivery struct {
	BotID, RoomID int64
	Endpoint      string
	Payload       []byte
}

func (s *NotificationQueries) Webhook(ctx context.Context, facts presentation.Facts, botID, messageID int64) (WebhookDelivery, error) {
	bot, err := s.DB.WebhookTarget(ctx, botID)
	if err != nil {
		return WebhookDelivery{}, err
	}
	message, err := s.DB.WebhookMessage(ctx, messageID)
	if err != nil {
		return WebhookDelivery{}, err
	}
	body := ""
	if message.HTML != nil {
		body = *message.HTML
	}
	plain, err := s.Content.PlainText(ctx, facts, body)
	if err != nil {
		return WebhookDelivery{}, err
	}
	if strings.TrimSpace(plain) == "" {
		if blob, err := s.DB.AttachedBlob(ctx, "Message", message.ID, "attachment"); err == nil {
			plain = blob.Filename
		}
	}
	plain = strings.TrimSpace(strings.ReplaceAll(plain, "@"+bot.Name, ""))
	payload, err := (integrations.WebhookContent{
		CreatorID: message.CreatorID, Creator: message.Creator, RoomID: message.RoomID, RoomName: message.RoomName,
		MessageID: message.ID, BotKey: fmt.Sprintf("%d-%s", bot.ID, bot.Token), HTML: message.HTML, Plain: plain,
	}).JSON()
	if err != nil {
		return WebhookDelivery{}, err
	}
	return WebhookDelivery{BotID: bot.ID, RoomID: message.RoomID, Endpoint: bot.Endpoint, Payload: payload}, nil
}
