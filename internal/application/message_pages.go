package application

import (
	"context"
	"database/sql"
	"errors"
	"html/template"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

// Observe captures an initial observation for callers without ingress metadata.
// A failed observation disables caching; later checks never relabel older data.
func (s *MessageQueries) Observe(ctx context.Context, facts presentation.Facts) presentation.MessageScope {
	generation, _ := s.DB.ResponseVersion(ctx)
	return presentation.MessageScope{Generation: generation, Facts: facts}
}
func (s *MessageQueries) checkObservation(ctx context.Context, scope presentation.MessageScope) presentation.MessageScope {
	if version, err := s.DB.ResponseVersion(ctx); err != nil || version != scope.Generation {
		scope.Generation = 0
	}
	return scope
}
func (s *MessageQueries) Page(ctx context.Context, scope presentation.MessageScope, user, room, anchor int64, direction string, fallback bool) (responsebody.Part, int, error) {
	read, err := s.DB.BeginMessageRead(ctx)
	if err != nil {
		return responsebody.Part{}, 0, err
	}
	defer read.Close()
	refs, err := read.PageReferences(ctx, user, room, anchor, direction)
	if fallback && errors.Is(err, sql.ErrNoRows) {
		refs, err = read.PageReferences(ctx, user, room, 0, direction)
	}
	if err != nil {
		return responsebody.Part{}, 0, err
	}
	part, err := s.readMessageList(ctx, scope, read, refs)
	return part, len(refs), err
}

func (s *MessageQueries) Search(ctx context.Context, scope presentation.MessageScope, user int64, query string) (responsebody.Part, int, error) {
	read, err := s.DB.BeginMessageRead(ctx)
	if err != nil {
		return responsebody.Part{}, 0, err
	}
	defer read.Close()
	refs, err := read.SearchReferences(ctx, user, query)
	if err != nil {
		return responsebody.Part{}, 0, err
	}
	part, err := s.readMessageList(ctx, scope, read, refs)
	return part, len(refs), err
}

// Select hits first, materialize only body/association misses in the selection
// snapshot, then release it before rich-text display, signing or template work.
func (s *MessageQueries) readMessageList(ctx context.Context, scope presentation.MessageScope, read *database.MessageRead, refs []database.MessageReference) (responsebody.Part, error) {
	scope = s.checkObservation(ctx, scope)
	if part, ok := s.Fragments.MessageList(scope, refs); ok {
		// Retain the actual Part: eviction cannot trigger new hydration.
		if err := read.Finish(); err != nil {
			return responsebody.Part{}, err
		}
		return part, nil
	}
	fragments := make([]template.HTML, len(refs))
	var missing []database.MessageReference
	var positions []int
	for i, ref := range refs {
		if html, ok := s.Fragments.Message(scope, ref); ok {
			fragments[i] = html
			continue
		}
		missing = append(missing, ref)
		positions = append(positions, i)
	}
	records, err := read.Records(ctx, missing)
	if err != nil {
		return responsebody.Part{}, err
	}
	prepared := presentation.PrepareMessages(records)
	data, mentions, err := read.Displays(ctx, prepared.Records, prepared.Mentioned)
	if err != nil {
		return responsebody.Part{}, err
	}
	if err = read.Finish(); err != nil {
		return responsebody.Part{}, err
	}
	scope = s.checkObservation(ctx, scope)
	views, err := s.Fragments.Messages(scope, prepared, data, mentions)
	if err != nil {
		return responsebody.Part{}, err
	}
	for i, view := range views {
		fragments[positions[i]] = view.Fragment
	}
	if len(refs) == 0 {
		return responsebody.Part{}, nil
	}
	// The admission key must use the same observation as the rendered misses.
	return s.Fragments.RecordMessages(scope, refs, fragments), nil
}
