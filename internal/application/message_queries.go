package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

type MessageQueries struct {
	DB           *database.DB
	Presentation *presentation.Renderer
	Fragments    *presentation.Fragments
	Content      *ContentQueries
}

// Views returns fully rendered fragments for captured records, including committed
// receipts. These records have no scoped observation and cannot reuse or admit
// query-cache fragments.
func (s *MessageQueries) Views(ctx context.Context, facts presentation.Facts, records []database.Message) ([]presentation.MessageView, error) {
	prepared := presentation.PrepareMessages(records)
	data, mentions, err := s.DB.MessageDisplays(ctx, prepared.Records, prepared.Mentioned)
	if err != nil {
		return nil, err
	}
	return s.Presentation.Messages(facts, prepared, data, mentions)
}

// CreatedStream renders a receipt's complete message directly into its append
// envelope. No intermediate message fragment is retained or admitted to caches.
func (s *MessageQueries) CreatedStream(ctx context.Context, facts presentation.Facts, commit database.MessageCommit) (string, error) {
	prepared := presentation.PrepareMessages([]database.Message{commit.Message})
	data, mentions, err := s.DB.MessageDisplays(ctx, prepared.Records, prepared.Mentioned)
	if err != nil {
		return "", err
	}
	views, err := s.Presentation.MessageViews(facts, prepared, data, mentions)
	if err != nil {
		return "", err
	}
	if data[commit.ID].Author == nil {
		return rails.TurboStream("append", commit.Room.DOM("messages"), string(views[0].Fragment)), nil
	}
	return s.Presentation.AppendMessage(commit.Room.DOM("messages"), views[0])
}

// Edit materializes only the attachment or editor inputs, not a displayed fragment.
func (s *MessageQueries) Edit(ctx context.Context, facts presentation.Facts, record database.Message) (presentation.MessageView, error) {
	view := presentation.ViewMessage(record)
	creators, err := s.DB.UserDisplays(ctx, []int64{record.CreatorID})
	if err != nil {
		return view, err
	}
	if _, exists := creators[record.CreatorID]; exists {
		blob, err := s.DB.AttachedBlob(ctx, "Message", record.ID, "attachment")
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return view, err
		}
		if err == nil {
			if err := s.Presentation.MessageAttachment(&view, blob); err != nil {
				return view, err
			}
		}
	}
	if view.Attachment == nil {
		doc := richtext.Prepare(record.Body)
		resolved, err := s.Content.Resolve(ctx, facts, doc.EditorAttachables())
		if err != nil {
			return view, err
		}
		view.Editable, _ = doc.Editable(resolved)
	}
	return view, nil
}

func (s *MessageQueries) Boosts(ctx context.Context, record database.Message) (presentation.MessageView, error) {
	view := presentation.ViewMessage(record)
	creators, err := s.DB.UserDisplays(ctx, []int64{record.CreatorID})
	if err != nil {
		return view, err
	}
	// Missing creators suppress boost presentation in the supported reference.
	if _, exists := creators[record.CreatorID]; exists {
		view.Boosts, err = s.DB.Boosts(ctx, record.ID)
	}
	return view, err
}
