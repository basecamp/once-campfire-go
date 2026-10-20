package web

import (
	"context"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

// HTTP copies its ingress observation and render facts into an owned query input.
func (s *Server) messageScope(ctx context.Context) presentation.MessageScope {
	facts := s.presentationFacts(ctx)
	if info := requestMetadata(ctx); info != nil {
		return presentation.MessageScope{Generation: info.databaseVersion, Facts: facts}
	}
	return s.MessageQueries.Observe(ctx, facts)
}
