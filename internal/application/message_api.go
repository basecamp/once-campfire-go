package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

type APIMessagePage struct {
	Messages []presentation.APIMessage
	Count    int
	Next     int64
}

func (s *MessageQueries) APIPage(ctx context.Context, facts presentation.Facts, user, room, anchor int64, direction string, linkAfter bool) (APIMessagePage, error) {
	read, err := s.DB.BeginMessageRead(ctx)
	if err != nil {
		return APIMessagePage{}, err
	}
	defer read.Close()
	records, count, next, err := read.APIPage(ctx, user, room, anchor, direction, linkAfter)
	if err != nil {
		return APIMessagePage{}, err
	}
	result := APIMessagePage{Count: count, Next: next}
	content := presentation.PrepareAPIContent(records)
	data, err := read.APIData(ctx, content.Records, content.Mentioned)
	if err != nil {
		return result, err
	}
	if err = read.Finish(); err != nil {
		return result, err
	}
	result.Messages, err = s.Presentation.APIMessages(facts, content, data)
	return result, err
}

// APIRecord prepares a captured command receipt, not a cacheable query observation.
func (s *MessageQueries) APIRecord(ctx context.Context, facts presentation.Facts, record database.Message) (presentation.APIMessage, error) {
	content := presentation.PrepareAPIContent([]database.Message{record})
	data, err := s.DB.MessageAPIData(ctx, content.Records, content.Mentioned)
	if err != nil {
		return presentation.APIMessage{}, err
	}
	messages, err := s.Presentation.APIMessages(facts, content, data)
	if err != nil {
		return presentation.APIMessage{}, err
	}
	return messages[0], nil
}
