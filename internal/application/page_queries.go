package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

// PageQueries materializes layout, navigation and room-list inputs. These are
// fresh observations, not a claim of one snapshot across an entire HTTP page.
type PageQueries struct{ DB *database.DB }

type LayoutRequest struct {
	UserID           int64
	Help, Navigation bool
	LastRoom         *int64
}
type LayoutData struct {
	Account     database.Account
	HelpContact database.UserContact
	ReturnRoom  *int64
}

func (q *PageQueries) Layout(ctx context.Context, input LayoutRequest) (LayoutData, error) {
	account, err := q.DB.Account(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LayoutData{}, err
	}
	data := LayoutData{Account: account}
	// Login help and return navigation have historically been best-effort. Their
	// failure must not turn a usable login/form/search representation into a 500.
	if input.Help {
		data.HelpContact, _ = q.DB.LoginHelpContact(ctx)
	}
	if input.Navigation {
		if id, err := q.LastRoom(ctx, input.UserID, input.LastRoom); err == nil {
			data.ReturnRoom = &id
		}
	}
	return data, nil
}

func (q *PageQueries) LastRoom(ctx context.Context, user int64, candidate *int64) (int64, error) {
	if candidate != nil {
		if _, err := q.DB.Room(ctx, user, *candidate); err == nil {
			return *candidate, nil
		}
	}
	return q.DB.OriginalRoom(ctx, user)
}

func (q *PageQueries) DisplayRoom(ctx context.Context, room database.Room, viewer database.RoomParticipant) (presentation.RoomView, error) {
	var members []database.RoomParticipant
	if room.Type == "Rooms::Direct" {
		var err error
		members, err = q.DB.RoomParticipants(ctx, room.ID)
		if err != nil {
			return presentation.RoomView{}, err
		}
	}
	return presentation.DisplayRoom(room, members, viewer), nil
}

type RoomPage struct {
	Room       presentation.RoomView
	Invitation bool
}

func (q *PageQueries) RoomPage(ctx context.Context, room database.Room, viewer database.RoomParticipant) (RoomPage, error) {
	view, err := q.DisplayRoom(ctx, room, viewer)
	if err != nil {
		return RoomPage{}, err
	}
	invitation, err := q.DB.RoomInvitation(ctx, room.ID)
	if err != nil {
		return RoomPage{}, err
	}
	return RoomPage{Room: view, Invitation: invitation}, nil
}

type ProfileData struct {
	Memberships, DirectMemberships []presentation.RoomView
	AvatarAttached                 bool
}

func (q *PageQueries) Profile(ctx context.Context, viewer database.RoomParticipant) (ProfileData, error) {
	rooms, err := q.DB.ProfileRooms(ctx, viewer.ID)
	if err != nil {
		return ProfileData{}, err
	}
	var data ProfileData
	_, err = q.DB.AttachedBlob(ctx, "User", viewer.ID, "avatar")
	data.AvatarAttached = err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProfileData{}, err
	}
	for _, room := range rooms {
		view := presentation.DisplayRoom(room.Room, room.Members, viewer)
		view.Involvement = room.Involvement
		if room.Type == "Rooms::Direct" {
			data.DirectMemberships = append(data.DirectMemberships, view)
		} else {
			data.Memberships = append(data.Memberships, view)
		}
	}
	return data, nil
}

type SidebarData struct {
	Rooms        []presentation.RoomView
	Placeholders []database.RoomParticipant
}

func (q *PageQueries) Sidebar(ctx context.Context, viewer database.RoomParticipant) (SidebarData, error) {
	rooms, err := q.DB.SidebarRooms(ctx, viewer.ID)
	if err != nil {
		return SidebarData{}, err
	}
	var data SidebarData
	for _, room := range rooms {
		view := presentation.DisplayRoom(room.Room, room.Members, viewer)
		view.Involvement, view.Unread = room.Involvement, room.Unread
		data.Rooms = append(data.Rooms, view)
	}
	data.Placeholders, err = q.DB.DirectPlaceholders(ctx, viewer.ID)
	return data, err
}
