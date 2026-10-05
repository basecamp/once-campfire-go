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
	if room.Type != "Rooms::Direct" {
		return sidebarRoom{Room: room}, nil
	}
	members, err := s.DB.RoomMembers(ctx, room.ID)
	if err != nil {
		return sidebarRoom{Room: room}, err
	}
	return directRoom(room, user, members), nil
}
func directRoom(room database.Room, user database.User, members []database.User) sidebarRoom {
	view := sidebarRoom{Room: room}
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
		return view
	}
	switch len(names) {
	case 1:
		view.Name = names[0]
	case 2:
		view.Name = names[0] + " and " + names[1]
	default:
		view.Name = strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
	return view
}
func (s *Server) sidebarRooms(ctx context.Context, user database.User) ([]sidebarRoom, error) {
	rooms, err := s.DB.SidebarRooms(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	var direct []int64
	for _, room := range rooms {
		if room.Type == "Rooms::Direct" {
			direct = append(direct, room.ID)
		}
	}
	members, err := s.DB.RoomMembersByRoom(ctx, direct)
	if err != nil {
		return nil, err
	}
	var result []sidebarRoom
	for _, room := range rooms {
		view := sidebarRoom{Room: room.Room}
		if room.Type == "Rooms::Direct" {
			view = directRoom(room.Room, user, members[room.ID])
		}
		view.Involvement, view.Unread = room.Involvement, room.Unread
		result = append(result, view)
	}
	return result, nil
}

func (s *Server) broadcastRoom(ctx context.Context, room database.Room, update bool) error {
	action, target := "prepend", "shared_rooms"
	if update {
		action, target = "replace", room.DOM("list")
	}
	if room.Type == "Rooms::Open" {
		markup, err := s.markup("sidebar-shared", sidebarRoom{Room: room})
		if err != nil {
			return err
		}
		s.Cable.PublishStream(ctx, "rooms", stream(action, target, markup))
		return nil
	}
	members, err := s.DB.Users(ctx, room.ID, false)
	if err != nil {
		return err
	}
	for _, user := range members {
		view, err := s.displayRoom(ctx, room, user)
		if err != nil {
			return err
		}
		name := "sidebar-shared"
		if room.Type == "Rooms::Direct" {
			name, target, action = "sidebar-direct", "direct_rooms", "prepend"
		}
		markup, err := s.markup(name, view)
		if err != nil {
			return err
		}
		s.Cable.PublishStream(ctx, rails.UserRoomsStream(user.ID), stream(action, target, markup))
	}
	return nil
}
