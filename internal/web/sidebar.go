package web

import (
	"context"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

type sidebarRoom struct {
	database.Room
	Members     []database.User
	Unread      bool
	Involvement string
}

func (r sidebarRoom) Label() string {
	if len(r.Members) == 1 {
		fields := strings.Fields(r.Members[0].Name)
		if len(fields) > 0 {
			return fields[0]
		}
		return ""
	}
	var names []string
	for _, member := range r.Members {
		var initials strings.Builder
		for i, word := range strings.Fields(member.Name) {
			if i >= 3 {
				break
			}
			initials.WriteString(strings.ToUpper(string([]rune(word)[0])))
		}
		names = append(names, initials.String())
	}
	if len(names) == 2 {
		return names[0] + "+" + names[1]
	}
	if len(names) > 2 {
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
	return strings.Join(names, "")
}
func (s *Server) displayRoom(ctx context.Context, room database.Room, user database.User) (sidebarRoom, error) {
	view := sidebarRoom{Room: room}
	if room.Type == "Rooms::Direct" {
		members, err := s.DB.RoomMembers(ctx, room.ID)
		if err != nil {
			return view, err
		}
		var names []string
		for _, member := range members {
			if member.ID != user.ID {
				view.Members = append(view.Members, member)
				names = append(names, member.Name)
			}
		}
		if len(view.Members) == 0 {
			view.Members = []database.User{user}
			view.Name = user.Name
		} else {
			switch len(names) {
			case 1:
				view.Name = names[0]
			case 2:
				view.Name = names[0] + " and " + names[1]
			default:
				view.Name = strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
			}
		}
	}
	return view, nil
}
func (s *Server) sidebarRooms(ctx context.Context, user database.User) ([]sidebarRoom, error) {
	rooms, err := s.DB.SidebarRooms(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	var result []sidebarRoom
	for _, room := range rooms {
		view, err := s.displayRoom(ctx, room.Room, user)
		if err != nil {
			return nil, err
		}
		view.Involvement, view.Unread = room.Involvement, room.Unread
		result = append(result, view)
	}
	return result, nil
}

// broadcastRoom streams a created or updated room's sidebar entry: users/sidebars/rooms/_shared
// to `:rooms` for an open room or to each member's `[user, :rooms]` for a closed one, and
// users/sidebars/rooms/_direct, rendered per membership, to each member of a direct room.
func (s *Server) broadcastRoom(ctx context.Context, room database.Room, update bool) error {
	action, target := "prepend", "shared_rooms"
	if update {
		action, target = "replace", room.DOM("list")
	}
	reference := database.ReferenceRoom{ID: room.ID, CreatorID: room.CreatorID, Name: database.NullString{String: room.Name, Valid: true}, Type: room.Type, UpdatedAt: room.UpdatedAt}
	switch room.Type {
	case "Rooms::Open":
		s.Cable.PublishStream(ctx, "rooms", stream(action, target, sharedRoomPartial(reference)))
	case "Rooms::Direct":
		partials, err := s.directRoomPartials(ctx, rendererBaseURLFrom(ctx), reference)
		if err != nil {
			return err
		}
		for _, partial := range partials {
			s.Cable.PublishStream(ctx, rails.UserRoomsStream(partial.Membership.UserID), stream("prepend", "direct_rooms", partial.HTML.HTML))
		}
	default:
		members, err := s.DB.Users(ctx, room.ID, false)
		if err != nil {
			return err
		}
		markup := sharedRoomPartial(reference)
		for _, user := range members {
			s.Cable.PublishStream(ctx, rails.UserRoomsStream(user.ID), stream(action, target, markup))
		}
	}
	return nil
}
