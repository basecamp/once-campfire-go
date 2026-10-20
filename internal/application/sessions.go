package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
)

// Sessions owns persistence/connection sequencing; cookies stay HTTP-owned.
type Sessions struct {
	DB    *database.DB
	Cable *cable.Hub
}

func (s *Sessions) Start(ctx context.Context, user int64, agent, ip string) (string, error) {
	return s.DB.StartSession(ctx, user, agent, ip)
}

func (s *Sessions) End(ctx context.Context, user int64, token, endpoint string) error {
	if err := s.DB.RevokeSession(ctx, user, token, endpoint); err != nil {
		return err
	}
	s.Cable.Disconnect(user)
	return nil
}
