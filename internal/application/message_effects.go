package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/presentation"
)

// MessageEffects is the shared browser/bot creation sequence. Required independent
// obligations precede fallible presentation, including for a processing failure.
// Trusted replies use the same notification/publication owners without admitting
// more webhooks; WebhookReplies does not depend on this coordinator.
type MessageEffects struct {
	Notifications *MessageNotifications
	Webhooks      *WebhookReplies
	Publications  *MessagePublications
}

func (s *MessageEffects) Created(ctx context.Context, facts presentation.Facts, result MessageResult) (string, error) {
	s.Notifications.Created(result.Commit.Message, result.Commit.Room)
	s.Webhooks.Enqueue(result.Commit.Message, result.Commit.Room)
	return s.Publications.Created(ctx, facts, result)
}
