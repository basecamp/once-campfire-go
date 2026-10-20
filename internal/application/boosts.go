package application

import (
	"context"
	"fmt"
	"time"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Boosts owns the shared browser/bot mutation and postcommit publication sequence.
// A command error means no commit; Processing never undoes the committed boost.
type Boosts struct {
	DB           *database.DB
	Presentation *presentation.Renderer
	Cable        *cable.Hub
}

type BoostResult struct {
	Commit     database.BoostCommit
	Processing error
}

func (s *Boosts) Create(ctx context.Context, actor, message int64, content string) (BoostResult, error) {
	commit, err := s.DB.CreateBoost(ctx, actor, message, content)
	if err != nil {
		return BoostResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	markup, err := s.Presentation.Markup("boost", commit.Boost)
	if err == nil {
		s.Cable.Publish(ctx, commit.RoomID, rails.TurboStream("append", "boosts_message_"+commit.ClientID, markup))
	}
	return BoostResult{Commit: commit, Processing: err}, nil
}

func (s *Boosts) Delete(ctx context.Context, actor, message, id int64) (database.BoostRemoval, error) {
	commit, err := s.DB.DeleteBoost(ctx, actor, message, id)
	if err != nil {
		return database.BoostRemoval{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.Cable.Publish(ctx, commit.RoomID, rails.TurboStream("remove", fmt.Sprintf("boost_%d", commit.ID), ""))
	return commit, nil
}
