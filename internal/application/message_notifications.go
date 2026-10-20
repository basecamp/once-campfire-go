package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

// MessageNotifications owns unread publication and push selection/admission.
// Neither obligation depends on media processing or message presentation success.
type MessageNotifications struct {
	DB      *database.DB
	Queries *NotificationQueries
	Cable   *cable.Hub
	Push    *integrations.PushSender
	Jobs    *jobs.Runner
}

func (s *MessageNotifications) Created(message database.Message, room database.Room) {
	s.unread(room.ID)
	if s.Push.VAPID == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deliveries, err := s.Queries.Push(ctx, presentation.Facts{Now: s.DB.Now()}, message, room)
	if err != nil {
		slog.Error("push preparation failed", "error", err)
		return
	}
	for _, delivery := range deliveries {
		if admission := s.Jobs.Enqueue(pushTask{notifications: s, delivery: delivery}); admission != jobs.Accepted {
			slog.Warn("push delivery not admitted", "admission", admission)
		}
	}
}

func (s *MessageNotifications) unread(room int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	members, err := s.DB.RoomMemberIDs(ctx, room)
	if err != nil {
		slog.Error("unread notification failed", "error", err)
		return
	}
	streams := make([]string, len(members))
	for i, id := range members {
		streams[i] = fmt.Sprintf("user_%d_unreads", id)
	}
	s.Cable.PublishStreams(ctx, map[string]any{"roomId": room}, streams...)
}

// The selected credentials and payload are an owned provider operation, not
// reloadable IDs: refetching here would change the pre-admission push policy.
type pushTask struct {
	notifications *MessageNotifications
	delivery      PushDelivery
}

func (pushTask) Queue() string { return "push" }
func (task pushTask) Run(ctx context.Context) error {
	s := task.notifications
	subscription := task.delivery.Subscription
	err := s.Push.Send(ctx, subscription.Endpoint, subscription.Key, subscription.Auth, task.delivery.Payload)
	if errors.Is(err, integrations.ErrPushGone) || errors.Is(err, integrations.ErrPushPoint) {
		return s.DB.DeletePushSubscription(ctx, subscription.UserID, subscription.ID)
	}
	return err
}
