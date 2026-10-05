package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func (s *Server) registerRoomRoutes() {
	for _, namespace := range []string{"opens", "closeds", "directs"} {
		prefix := "/rooms/" + namespace
		forms := s.roomForms(namespace)
		s.mux.HandleFunc("GET "+prefix+"/new", s.auth(forms.new))
		s.mux.HandleFunc("POST "+prefix, s.auth(forms.create))
		s.mux.HandleFunc("GET "+prefix+"/{id}/edit", s.auth(forms.edit))
		s.mux.HandleFunc("GET "+prefix+"/{id}", s.auth(s.redirectRoom))
		if forms.update != nil {
			s.mux.HandleFunc("PATCH "+prefix+"/{id}", s.auth(forms.update))
			s.mux.HandleFunc("PUT "+prefix+"/{id}", s.auth(forms.update))
		}
		if forms.destroy != nil {
			s.mux.HandleFunc("DELETE "+prefix+"/{id}", s.auth(forms.destroy))
		}
	}
	s.mux.HandleFunc("DELETE /rooms/{id}", s.auth(s.deleteRoom))
	s.mux.HandleFunc("GET /rooms/{room_id}/involvement", s.auth(s.involvementShow))
	s.mux.HandleFunc("PATCH /rooms/{room_id}/involvement", s.auth(s.involvementUpdate))
	s.mux.HandleFunc("PUT /rooms/{room_id}/involvement", s.auth(s.involvementUpdate))
	s.mux.HandleFunc("GET /rooms/{id}/{anchor}", s.auth(s.roomShow))
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
	if u.Role != 1 && room.CreatorID != u.ID {
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
