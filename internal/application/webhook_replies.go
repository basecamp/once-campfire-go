package application

import (
	"bytes"
	"context"
	"log/slog"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// WebhookReplies owns admission, provider delivery and the distinct trusted reply.
// Replies reuse notifications/publications, never normal webhook admission; this
// keeps no-loop policy explicit without a MessageEffects dependency cycle.
type WebhookReplies struct {
	Queries       *NotificationQueries
	Client        *integrations.WebhookClient
	Storage       *storage.Store
	Commands      *Messages
	Notifications *MessageNotifications
	Publications  *MessagePublications
	Jobs          *jobs.Runner
}

func (s *WebhookReplies) Enqueue(message database.Message, room database.Room) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bots, err := s.Queries.WebhookRecipients(ctx, presentation.Facts{Now: s.Queries.DB.Now()}, message, room)
	if err != nil {
		slog.Error("webhook recipients failed", "error", err)
		return
	}
	for _, bot := range bots {
		if admission := s.Jobs.Enqueue(webhookTask{replies: s, botID: bot, messageID: message.ID}); admission != jobs.Accepted {
			slog.Warn("webhook delivery not admitted", "admission", admission)
		}
	}
}

type webhookTask struct {
	replies          *WebhookReplies
	botID, messageID int64
}

func (webhookTask) Queue() string { return "webhook" }
func (task webhookTask) Run(ctx context.Context) error {
	return task.replies.Deliver(ctx, task.botID, task.messageID)
}

func (s *WebhookReplies) Deliver(ctx context.Context, botID, messageID int64) error {
	delivery, err := s.Queries.Webhook(ctx, presentation.Facts{Now: s.Queries.DB.Now()}, botID, messageID)
	if err != nil {
		return err
	}
	reply, err := s.Client.Deliver(ctx, delivery.Endpoint, delivery.Payload)
	if err != nil {
		return err
	}
	if reply.Text == nil && reply.Filename == "" {
		return nil
	}
	var staged *storage.Staged
	if reply.Text == nil {
		contentType := storage.Identify(reply.Attachment, reply.Filename, reply.ContentType)
		staged, err = s.Storage.StageFile(ctx, reply.Filename, contentType, bytes.NewReader(reply.Attachment))
		if err != nil {
			return err
		}
	}
	result, err := s.Commands.Reply(ctx, delivery.BotID, delivery.RoomID, reply.Text, staged)
	if err != nil {
		return err
	}
	s.Notifications.Created(result.Commit.Message, result.Commit.Room)
	_, err = s.Publications.Created(ctx, presentation.Facts{}, result)
	return err
}
