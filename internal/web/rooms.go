package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func (s *Server) registerRoomRoutes() {
	for _, namespace := range []string{"opens", "closeds", "directs"} {
		prefix := "/rooms/" + namespace
		s.mux.HandleFunc("GET "+prefix+"/new", s.auth(s.roomForm))
		s.mux.HandleFunc("POST "+prefix, s.auth(s.saveRoom))
		s.mux.HandleFunc("GET "+prefix+"/{id}/edit", s.auth(s.roomForm))
		s.mux.HandleFunc("GET "+prefix+"/{id}", s.auth(s.redirectRoom))
		s.mux.HandleFunc("PATCH "+prefix+"/{id}", s.auth(s.saveRoom))
		s.mux.HandleFunc("PUT "+prefix+"/{id}", s.auth(s.saveRoom))
		s.mux.HandleFunc("DELETE "+prefix+"/{id}", s.auth(s.deleteRoom))
	}
	s.mux.HandleFunc("DELETE /rooms/{id}", s.auth(s.deleteRoom))
	s.mux.HandleFunc("GET /rooms/{id}/involvement", s.auth(s.involvement))
	s.mux.HandleFunc("PATCH /rooms/{id}/involvement", s.auth(s.involvement))
	s.mux.HandleFunc("PUT /rooms/{id}/involvement", s.auth(s.involvement))
	s.mux.HandleFunc("GET /rooms/{id}/{anchor}", s.auth(s.roomAt))
}
func (s *Server) roomLookupFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, database.ErrNoRows) || errors.Is(err, database.ErrForbidden) {
		s.flash(r, "alert", "Room not found or inaccessible")
		http.Redirect(w, r, "/", http.StatusFound)
	} else {
		s.fail(w, err)
	}
}
func namespaceKind(r *http.Request) string {
	switch strings.Split(r.URL.Path, "/")[2] {
	case "closeds":
		return "Rooms::Closed"
	case "directs":
		return "Rooms::Direct"
	default:
		return "Rooms::Open"
	}
}
func roomUsers(r *http.Request) []int64 {
	var ids []int64
	for _, value := range append(r.Form["user_ids[]"], r.Form["user_ids"]...) {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
func (s *Server) canCreateRoom(ctx context.Context, u database.User, kind string) error {
	if kind == "Rooms::Direct" || u.Role == 1 {
		return nil
	}
	account, err := s.DB.Account(ctx)
	if err != nil {
		return err
	}
	if account.RestrictRooms() {
		return database.ErrForbidden
	}
	return nil
}
func (s *Server) roomForm(w http.ResponseWriter, r *http.Request, u database.User) {
	kind := namespaceKind(r)
	room := database.Room{Type: kind, CreatorID: u.ID, Name: "New room"}
	var err error
	if roomID(r) != 0 {
		room, err = s.DB.Room(r.Context(), u.ID, roomID(r))
		if err == nil && (kind == "Rooms::Direct") != (room.Type == "Rooms::Direct") {
			err = database.ErrForbidden
		}
	} else {
		err = s.canCreateRoom(r.Context(), u, kind)
	}
	if err != nil {
		if roomID(r) != 0 {
			s.roomLookupFailure(w, r, err)
		} else {
			s.fail(w, err)
		}
		return
	}
	room.Type = kind
	users, err := s.DB.Users(r.Context(), 0, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	selected := map[int64]bool{u.ID: true}
	if room.ID != 0 {
		members, err := s.DB.Users(r.Context(), room.ID, false)
		if err != nil {
			s.fail(w, err)
			return
		}
		selected = map[int64]bool{}
		for _, m := range members {
			selected[m.ID] = true
		}
	}
	divider := 0
	if room.ID != 0 && kind == "Rooms::Closed" {
		ordered := make([]database.User, 0, len(users))
		for _, member := range users {
			if selected[member.ID] {
				ordered = append(ordered, member)
			}
		}
		divider = len(ordered)
		for _, member := range users {
			if !selected[member.ID] {
				ordered = append(ordered, member)
			}
		}
		users = ordered
		if divider == len(users) {
			divider = 0
		}
	}
	if room.ID != 0 && kind == "Rooms::Direct" {
		members, e := s.DB.RoomMembers(r.Context(), room.ID)
		if e != nil {
			s.fail(w, e)
			return
		}
		users = nil
		for _, member := range members {
			if len(members) == 1 || member.ID != u.ID {
				users = append(users, member)
			}
		}
	}
	s.render(w, r, "room-form", 200, page{UserDivider: divider, Title: "Room settings", User: u, Room: room, Users: users, Selected: selected, CanAdminister: room.ID == 0 || u.Role == 1 || room.CreatorID == u.ID || kind == "Rooms::Direct"})
}
func (s *Server) saveRoom(w http.ResponseWriter, r *http.Request, u database.User) {
	kind := namespaceKind(r)
	id := roomID(r)
	updating := id != 0
	name := r.Form.Get("room[name]")
	if id == 0 {
		if err := s.canCreateRoom(r.Context(), u, kind); err != nil {
			s.fail(w, err)
			return
		}
		room, err := s.DB.CreateRoom(r.Context(), u.ID, kind, name, roomUsers(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		id = room.ID
	} else {
		room, err := s.DB.Room(r.Context(), u.ID, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		if room.Type == "Rooms::Direct" || kind == "Rooms::Direct" {
			s.roomLookupFailure(w, r, database.ErrNoRows)
			return
		}
		if u.Role != 1 && room.CreatorID != u.ID {
			s.fail(w, database.ErrForbidden)
			return
		}
		if err = s.DB.UpdateRoom(r.Context(), id, kind, name, roomUsers(r)); err != nil {
			s.fail(w, err)
			return
		}
	}
	room, err := s.DB.FindRoom(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.broadcastRoom(r.Context(), room, updating); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/rooms/%d", id), 302)
}
func (s *Server) redirectRoom(w http.ResponseWriter, r *http.Request, u database.User) {
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.roomLookupFailure(w, r, err)
		return
	}
	if (namespaceKind(r) == "Rooms::Direct") != (room.Type == "Rooms::Direct") {
		s.roomLookupFailure(w, r, database.ErrNoRows)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/rooms/%d", room.ID), 302)
}
func (s *Server) deleteRoom(w http.ResponseWriter, r *http.Request, u database.User) {
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.roomLookupFailure(w, r, err)
		return
	}
	directNamespace := strings.HasPrefix(r.URL.Path, "/rooms/directs/")
	if directNamespace && room.Type != "Rooms::Direct" {
		s.roomLookupFailure(w, r, database.ErrNoRows)
		return
	}
	if !directNamespace && u.Role != 1 && room.CreatorID != u.ID {
		s.fail(w, database.ErrForbidden)
		return
	}
	if err = s.DB.DeleteRoom(r.Context(), room.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.Cable.PublishStream(r.Context(), "rooms", stream("remove", room.DOM("list"), ""))
	http.Redirect(w, r, "/", 302)
}
func (s *Server) involvement(w http.ResponseWriter, r *http.Request, u database.User) {
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		previous, e := s.DB.Involvement(r.Context(), u.ID, room.ID)
		if e != nil {
			s.fail(w, e)
			return
		}
		value := r.Form.Get("involvement")
		if err = s.DB.SetInvolvement(r.Context(), u.ID, room.ID, r.Form.Get("involvement")); err != nil {
			s.fail(w, err)
			return
		}
		if room.Type != "Rooms::Direct" {
			if value == "invisible" {
				s.Cable.PublishStream(r.Context(), rails.UserRoomsStream(u.ID), stream("remove", room.DOM("list"), ""))
			} else if previous == "invisible" {
				markup, err := s.markup("sidebar-shared", sidebarRoom{Room: room})
				if err != nil {
					s.fail(w, err)
					return
				}
				s.Cable.PublishStream(r.Context(), rails.UserRoomsStream(u.ID), stream("prepend", "shared_rooms", markup))
			}
		}
		http.Redirect(w, r, fmt.Sprintf("/rooms/%d/involvement", room.ID), 302)
		return
	}
	value, err := s.DB.Involvement(r.Context(), u.ID, room.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "involvement", 200, page{User: u, Room: room, Involvement: value})
}
func (s *Server) roomAt(w http.ResponseWriter, r *http.Request, u database.User) {
	if !strings.HasPrefix(r.PathValue("anchor"), "@") {
		http.NotFound(w, r)
		return
	}
	s.room(w, r, u)
}

func (s *Server) roomsIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	var id int64
	if err := s.DB.Read.QueryRowContext(r.Context(), "SELECT room_id FROM memberships WHERE user_id=? ORDER BY room_id DESC LIMIT 1", u.ID).Scan(&id); err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/rooms/%d", s.origin(r), id), 302)
}
