package application

import (
	"context"
	"database/sql"

	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
)

type Rooms struct {
	DB      *database.DB
	Cable   *cable.Hub
	Cleanup *Cleanup
}

type RoomResult struct {
	Commit     database.RoomCommit
	Processing error
}

func (s *Rooms) Involvement(ctx context.Context, user, room int64, value string) (database.InvolvementCommit, error) {
	return s.DB.ChangeInvolvement(ctx, user, room, value)
}

func (s *Rooms) Create(ctx context.Context, actor int64, kind string, name *sql.NullString, users []int64) (database.Room, error) {
	return s.DB.CreateRoom(ctx, actor, kind, name, users)
}

func (s *Rooms) Update(ctx context.Context, actor, id int64, kind string, name *sql.NullString, users []int64) (RoomResult, error) {
	commit, err := s.DB.UpdateRoom(ctx, actor, id, kind, name, users)
	if err != nil {
		return RoomResult{}, err
	}
	for _, user := range commit.Revoked {
		s.Cable.Reconnect(user)
	}
	return RoomResult{Commit: commit}, nil
}

func (s *Rooms) Delete(ctx context.Context, actor, id int64) (RoomResult, error) {
	commit, err := s.DB.DeleteRoom(ctx, actor, id)
	if err != nil {
		return RoomResult{}, err
	}
	return RoomResult{Commit: commit, Processing: s.Cleanup.Detached(commit.Detached)}, nil
}

func (s *Rooms) DeleteDirect(ctx context.Context, actor, id int64) (RoomResult, error) {
	commit, err := s.DB.DeleteDirectRoom(ctx, actor, id)
	if err != nil {
		return RoomResult{}, err
	}
	return RoomResult{Commit: commit, Processing: s.Cleanup.Detached(commit.Detached)}, nil
}
