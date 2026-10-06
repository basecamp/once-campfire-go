package web

import (
	"context"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastdb"
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

// displayRoom builds the sidebar's room view, loading the members of direct
// rooms through fastdb when c is set (it is passed by the room and sidebar
// handlers; read-path call sites outside those pass nil and use database/sql).
func (s *Server) displayRoom(c *fastdb.Conn, ctx context.Context, room database.Room, user database.User) (sidebarRoom, error) {
	view := sidebarRoom{Room: room}
	if room.Type == "Rooms::Direct" {
		var members []database.User
		if c != nil {
			fast, err := c.RoomMembers(nil, room.ID)
			if err != nil {
				return view, err
			}
			members = usersOf(fast)
		} else {
			var err error
			members, err = s.DB.RoomMembers(ctx, room.ID)
			if err != nil {
				return view, err
			}
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
func (s *Server) sidebarRooms(c *fastdb.Conn, ctx context.Context, user database.User) ([]sidebarRoom, error) {
	var rooms []database.SidebarRoom
	if c != nil {
		fast, err := c.SidebarRooms(nil, user.ID)
		if err != nil {
			return nil, err
		}
		rooms = make([]database.SidebarRoom, 0, len(fast))
		for _, room := range fast {
			rooms = append(rooms, sidebarRoomOf(room))
		}
	} else {
		var err error
		rooms, err = s.DB.SidebarRooms(ctx, user.ID)
		if err != nil {
			return nil, err
		}
	}
	var result []sidebarRoom
	for _, room := range rooms {
		view, err := s.displayRoom(c, ctx, room.Room, user)
		if err != nil {
			return nil, err
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
		view, err := s.displayRoom(nil, ctx, room, user)
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
