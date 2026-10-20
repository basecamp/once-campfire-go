package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

// Searches owns query normalization, history commands and prepared search pages.
// History and matching messages remain separate observations; scoped message
// selection/body/association coherence belongs to MessageQueries.
type Searches struct {
	DB       *database.DB
	Messages *MessageQueries
}

func (s *Searches) Remember(ctx context.Context, user int64, input string) (string, error) {
	query := database.SearchQuery(input)
	return query, s.DB.RecordSearch(ctx, user, query)
}

func (s *Searches) Clear(ctx context.Context, user int64) error {
	return s.DB.ClearSearches(ctx, user)
}

type SearchPage struct {
	Query    string
	Recent   []string
	Messages responsebody.Part
	Count    int
}

func (s *Searches) Page(ctx context.Context, scope presentation.MessageScope, user int64, input string) (SearchPage, error) {
	page := SearchPage{Query: database.SearchQuery(input)}
	var err error
	page.Recent, err = s.DB.RecentSearches(ctx, user)
	if err != nil {
		return SearchPage{}, err
	}
	page.Messages, page.Count, err = s.Messages.Search(ctx, scope, user, page.Query)
	if err != nil {
		return SearchPage{}, err
	}
	return page, nil
}
