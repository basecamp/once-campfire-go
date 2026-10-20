package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/database"
)

func (s *Server) roomLookupFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, database.ErrForbidden) {
		s.flash(r, "alert", "Room not found or inaccessible")
		http.Redirect(w, r, "/", http.StatusFound)
	} else {
		s.fail(w, err)
	}
}

func namespaceKind(r *http.Request) string {
	_, params, _ := recognizeRequest(r)
	switch params["controller"] {
	case "rooms/closeds":
		return "Rooms::Closed"
	case "rooms/directs":
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

func (s *Server) roomForm(w http.ResponseWriter, r *http.Request, u database.User) {
	id := roomID(r)
	data, err := s.RoomQueries.Form(r.Context(), application.RoomFormRequest{
		UserID: u.ID, Role: u.Role, RoomID: id, Kind: namespaceKind(r),
	})
	if err != nil {
		if id != 0 {
			s.roomLookupFailure(w, r, err)
		} else {
			s.fail(w, err)
		}
		return
	}
	s.respondPage(w, r, "room-form", 200, page{
		Title: "Room settings", User: u, Room: data.Room, RoomUsers: data.Users,
		UserDivider: data.Divider, CanAdminister: data.CanAdminister,
	})
}

// Strong room attributes distinguish omitted names, explicit null, and empty text.
func roomName(r *http.Request) (*sql.NullString, error) {
	params, _ := r.Context().Value(structuredParamsKey{}).(map[string]any)
	room := params["room"]
	switch value := room.(type) {
	case nil:
		return nil, fmt.Errorf("missing room parameter")
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("missing room parameter")
		}
	case []any:
		if len(value) == 0 {
			return nil, fmt.Errorf("missing room parameter")
		}
	case map[string]any:
		if len(value) == 0 {
			return nil, fmt.Errorf("missing room parameter")
		}
		name, submitted := value["name"]
		if !submitted {
			return nil, nil
		}
		switch name := name.(type) {
		case string:
			return &sql.NullString{String: name, Valid: true}, nil
		case nil, bool, json.Number:
			return &sql.NullString{}, nil
		}
	}
	return nil, nil
}

func (s *Server) saveRoom(w http.ResponseWriter, r *http.Request, u database.User) {
	kind := namespaceKind(r)
	id := roomID(r)
	updating := id != 0
	var saved database.Room
	// Preflight preserves protocol error ordering. Mutation authority is checked
	// again against current state inside the command transaction.
	if id == 0 {
		if err := s.RoomQueries.CanCreate(r.Context(), u.Role, kind); err != nil {
			s.fail(w, err)
			return
		}
		var name *sql.NullString
		if kind != "Rooms::Direct" {
			var err error
			name, err = roomName(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		room, err := s.RoomCommands.Create(r.Context(), u.ID, kind, name, roomUsers(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		id = room.ID
		saved = room
	} else {
		room, err := s.DB.Room(r.Context(), u.ID, id)
		if err != nil {
			s.roomLookupFailure(w, r, err)
			return
		}
		if room.Type == "Rooms::Direct" || kind == "Rooms::Direct" {
			s.roomLookupFailure(w, r, sql.ErrNoRows)
			return
		}
		if u.Role != 1 && room.CreatorID != u.ID {
			s.fail(w, database.ErrForbidden)
			return
		}
		name, err := roomName(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := s.RoomCommands.Update(r.Context(), u.ID, id, kind, name, roomUsers(r))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				s.roomLookupFailure(w, r, err)
			} else {
				s.fail(w, err)
			}
			return
		}
		saved = result.Commit.Room
	}
	if err := s.RoomPublications.Saved(r.Context(), saved, updating); err != nil {
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
		s.roomLookupFailure(w, r, sql.ErrNoRows)
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
	directNamespace := r.PathValue("controller") == "rooms/directs"
	if directNamespace && room.Type != "Rooms::Direct" {
		s.roomLookupFailure(w, r, sql.ErrNoRows)
		return
	}
	if !directNamespace && u.Role != 1 && room.CreatorID != u.ID {
		s.fail(w, database.ErrForbidden)
		return
	}
	var result application.RoomResult
	if directNamespace {
		result, err = s.RoomCommands.DeleteDirect(r.Context(), u.ID, room.ID)
	} else {
		result, err = s.RoomCommands.Delete(r.Context(), u.ID, room.ID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.roomLookupFailure(w, r, err)
		} else {
			s.fail(w, err)
		}
		return
	}
	if err := s.RoomPublications.Removed(r.Context(), result); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/", 302)
}

func (s *Server) involvement(w http.ResponseWriter, r *http.Request, u database.User) {
	if r.Method != "GET" && r.Method != "HEAD" {
		commit, err := s.RoomCommands.Involvement(r.Context(), u.ID, roomID(r), r.Form.Get("involvement"))
		if err != nil {
			s.fail(w, err)
			return
		}
		if err = s.RoomPublications.Involvement(r.Context(), u.ID, commit); err != nil {
			s.fail(w, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/rooms/%d/involvement", commit.ID), 302)
		return
	}
	state, err := s.RoomQueries.Involvement(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.respondPage(w, r, "involvement-page", 200, page{User: u, Room: state.Room, Involvement: state.Value})
}

func (s *Server) roomsIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	id, err := s.RoomQueries.Index(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/rooms/%d", s.origin(r), id), 302)
}
