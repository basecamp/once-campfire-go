package application

import (
	"context"
	"errors"
	"time"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// MessagePublications prepares and publishes committed messages without retaining
// HTTP state. Notification preparation and transport each own separate deadlines.
type MessagePublications struct {
	Queries *MessageQueries
	Cable   *cable.Hub
}

func (s *MessagePublications) Created(ctx context.Context, facts presentation.Facts, result MessageResult) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	facts.Now = s.Queries.DB.Now()
	commit := result.Commit
	output, err := s.Queries.CreatedStream(ctx, facts, commit)
	if err != nil {
		return "", errors.Join(result.Processing, err)
	}
	s.publish(commit.Room.ID, output)
	return output, result.Processing
}

func (s *MessagePublications) Updated(ctx context.Context, facts presentation.Facts, result MessageResult) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	facts.Now = s.Queries.DB.Now()
	message := result.Commit.Message
	views, err := s.Queries.Views(ctx, facts, []database.Message{message})
	if err != nil {
		return errors.Join(result.Processing, err)
	}
	markup, err := s.Queries.Presentation.Markup("presentation", views[0])
	if err != nil {
		return errors.Join(result.Processing, err)
	}
	s.publish(message.RoomID, rails.TurboStream("replace", "presentation_message_"+message.ClientID, markup))
	return result.Processing
}

func (s *MessagePublications) Removed(ctx context.Context, result MessageResult) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	message := result.Commit.Message
	output := rails.TurboStream("remove", "message_"+message.ClientID, "")
	s.Cable.Publish(ctx, message.RoomID, output)
	return output, result.Processing
}

func (s *MessagePublications) publish(room int64, output string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Cable.Publish(ctx, room, output)
}
