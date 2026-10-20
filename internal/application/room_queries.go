package application

import (
	"context"

	"github.com/basecamp/once-campfire-go/internal/database"
)

type RoomQueries struct{ DB *database.DB }

func (q *RoomQueries) Involvement(ctx context.Context, user, room int64) (database.RoomInvolvement, error) {
	return q.DB.RoomInvolvement(ctx, user, room)
}

func (q *RoomQueries) Index(ctx context.Context, user int64) (int64, error) {
	return q.DB.NewestRoom(ctx, user)
}

type RoomFormRequest struct {
	UserID, RoomID int64
	Role           int
	Kind           string
}

type RoomForm struct {
	Room          database.Room
	Users         []database.RoomChoice
	Divider       int
	CanAdminister bool
}

// CanCreate preserves the early form/decoder policy. Commands still check
// authoritative permission again in their writer transaction.
func (q *RoomQueries) CanCreate(ctx context.Context, role int, kind string) error {
	if kind == "Rooms::Direct" || role == 1 {
		return nil
	}
	account, err := q.DB.Account(ctx)
	if err != nil {
		return err
	}
	if account.RestrictRooms() {
		return database.ErrForbidden
	}
	return nil
}

func (q *RoomQueries) Form(ctx context.Context, input RoomFormRequest) (RoomForm, error) {
	room := database.Room{Type: input.Kind, CreatorID: input.UserID, Name: "New room"}
	if input.RoomID != 0 {
		var err error
		room, err = q.DB.Room(ctx, input.UserID, input.RoomID)
		if err != nil {
			return RoomForm{}, err
		}
		if (input.Kind == "Rooms::Direct") != (room.Type == "Rooms::Direct") {
			return RoomForm{}, database.ErrForbidden
		}
	} else if err := q.CanCreate(ctx, input.Role, input.Kind); err != nil {
		return RoomForm{}, err
	}
	// The namespace is also the form's proposed open/closed type, not just a
	// display of the existing type. Switching it must keep the current members.
	room.Type = input.Kind
	data := RoomForm{Room: room, CanAdminister: room.ID == 0 || input.Role == 1 || room.CreatorID == input.UserID || input.Kind == "Rooms::Direct"}
	if input.Kind == "Rooms::Direct" {
		if room.ID == 0 {
			return data, nil
		}
		members, err := q.DB.RoomMemberDisplays(ctx, room.ID)
		if err != nil {
			return RoomForm{}, err
		}
		for _, member := range members {
			if len(members) > 1 && member.ID == input.UserID {
				continue
			}
			data.Users = append(data.Users, database.RoomChoice{UserDisplay: member})
		}
		return data, nil
	}
	var err error
	data.Users, err = q.DB.RoomChoices(ctx, room.ID, input.UserID)
	if err != nil {
		return RoomForm{}, err
	}
	if room.ID != 0 && input.Kind == "Rooms::Closed" {
		ordered := make([]database.RoomChoice, 0, len(data.Users))
		for _, member := range data.Users {
			if member.Selected {
				ordered = append(ordered, member)
			}
		}
		data.Divider = len(ordered)
		for _, member := range data.Users {
			if !member.Selected {
				ordered = append(ordered, member)
			}
		}
		data.Users = ordered
		if data.Divider == len(data.Users) {
			data.Divider = 0
		}
	}
	return data, nil
}
