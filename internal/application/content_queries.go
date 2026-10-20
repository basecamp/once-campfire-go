package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
)

// ContentQueries materializes consumer-specific mention projections before pure
// transformations. Infrastructure failures never become malformed-content fallback.
type ContentQueries struct {
	DB      *database.DB
	Secrets *rails.Secrets
}

func (s *ContentQueries) Resolve(ctx context.Context, facts presentation.Facts, tokens []string) (richtext.Context, error) {
	targets := make(map[string]presentation.MentionTarget, len(tokens))
	ids := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		target := presentation.MentionTargetFor(token)
		targets[token] = target
		if target.ID != 0 {
			ids = append(ids, target.ID)
		}
	}
	users, err := s.DB.UserDisplays(ctx, ids)
	if err != nil {
		return richtext.Context{}, err
	}
	return presentation.MentionContext(s.Secrets, facts.Host, facts.Now, targets, users), nil
}

func (s *ContentQueries) Content(ctx context.Context, facts presentation.Facts, body string) (richtext.Result, error) {
	doc := richtext.Prepare(body)
	resolved, err := s.Resolve(ctx, facts, doc.Attachables())
	if err != nil {
		return richtext.Result{}, err
	}
	return doc.Content(resolved)
}

func (s *ContentQueries) PlainText(ctx context.Context, facts presentation.Facts, body string) (string, error) {
	doc := richtext.Prepare(body)
	resolved, err := s.Resolve(ctx, facts, doc.PlainAttachables())
	if err != nil {
		return "", err
	}
	// Only malformed-content errors remain after preparation.
	plain, _ := doc.PlainText(resolved)
	return plain, nil
}

func (s *ContentQueries) MentionedIDs(ctx context.Context, facts presentation.Facts, body string) ([]int64, error) {
	doc := richtext.Prepare(body)
	var ids []int64
	for _, token := range doc.MentionAttachables() {
		if id := presentation.VerifiedMentionID(s.Secrets, token, facts.Now); id != 0 {
			ids = append(ids, id)
		}
	}
	users, err := s.DB.UserDisplays(ctx, ids)
	if err != nil {
		return nil, err
	}
	resolved := presentation.MentionContext(s.Secrets, facts.Host, facts.Now, nil, users)
	ids, _ = doc.MentionIDs(resolved)
	return ids, nil
}
