package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func (s *Server) registerMessageRoutes() {
	s.mux.HandleFunc("GET /messages", s.auth(s.messages))
	s.mux.HandleFunc("POST /messages", s.auth(s.createMessage))
	s.mux.HandleFunc("GET /messages/{message}", s.auth(s.showMessage))
	s.mux.HandleFunc("GET /messages/{message}/edit", s.auth(s.editMessage))
	s.mux.HandleFunc("PATCH /messages/{message}", s.auth(s.updateMessage))
	s.mux.HandleFunc("PUT /messages/{message}", s.auth(s.updateMessage))
	s.mux.HandleFunc("DELETE /messages/{message}", s.auth(s.deleteMessage))
	s.mux.HandleFunc("GET /rooms/{id}/messages/{message}", s.auth(s.showMessage))
	s.mux.HandleFunc("GET /rooms/{id}/messages/{message}/edit", s.auth(s.editMessage))
	s.mux.HandleFunc("PATCH /rooms/{id}/messages/{message}", s.auth(s.updateMessage))
	s.mux.HandleFunc("PUT /rooms/{id}/messages/{message}", s.auth(s.updateMessage))
	s.mux.HandleFunc("DELETE /rooms/{id}/messages/{message}", s.auth(s.deleteMessage))
	s.mux.HandleFunc("GET /messages/{message}/boosts", s.auth(s.boosts))
	s.mux.HandleFunc("GET /messages/{message}/boosts/new", s.auth(s.newBoost))
	s.mux.HandleFunc("POST /messages/{message}/boosts", s.auth(s.createBoost))
	s.mux.HandleFunc("DELETE /messages/{message}/boosts/{boost}", s.auth(s.deleteBoost))
	s.mux.HandleFunc("GET /rooms/{id}/refresh", s.auth(s.refreshRoom))
}
func pathInt(r *http.Request, key string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(key), 10, 64)
	return id
}
func (s *Server) findMessage(r *http.Request, u database.User, administer bool) (database.Message, error) {
	if route, _, _ := recognize(r.Method, r.URL.EscapedPath()); route != nil && strings.HasPrefix(route.Endpoint, "messages#") && roomID(r) == 0 {
		return database.Message{}, sql.ErrNoRows
	}
	m, err := s.DB.ReachableMessage(r.Context(), u.ID, pathInt(r, "message"))
	if err != nil {
		return m, err
	}
	if (r.PathValue("room_id") != "" || r.Form.Has("room_id")) && m.RoomID != roomID(r) {
		return m, sql.ErrNoRows
	}
	if administer && u.Role != 1 && u.ID != m.CreatorID {
		return m, database.ErrForbidden
	}
	return m, nil
}
func (s *Server) messageViews(ctx context.Context, messages []database.Message) ([]messageView, error) {
	views := viewMessages(messages)
	roomNames := map[int64]string{}
	creators := map[int64]database.User{}
	for i := range views {
		name, ok := roomNames[views[i].RoomID]
		if !ok {
			room, err := s.DB.FindRoom(ctx, views[i].RoomID)
			if err != nil {
				return nil, err
			}
			name = room.Name
			if room.Type == "Rooms::Direct" {
				view, err := s.displayRoom(ctx, room, database.User{})
				if err != nil {
					return nil, err
				}
				name = view.Name
			}
			roomNames[room.ID] = name
		}
		creator, found := creators[views[i].CreatorID]
		if !found {
			var err error
			creator, err = s.DB.User(ctx, views[i].CreatorID)
			if errors.Is(err, sql.ErrNoRows) {
				views[i].Fragment = unrenderableMessage
				continue
			}
			if err != nil {
				return nil, err
			}
			creators[creator.ID] = creator
		}
		views[i].CreatorTitle = creator.Title()
		views[i].CreatorUpdatedAt = creator.UpdatedAt
		views[i].Permalink = messagePermalink(ctx, views[i].RoomID, views[i].ID)
		views[i].RoomName = name
		result, _ := richtext.Display(views[i].Body, s.richContext(ctx))
		views[i].HTML = template.HTML(result.Presentation)
		views[i].AllEmoji = allEmoji(result.Plain)
		if sound := soundHTML(result.Plain); sound != "" {
			views[i].HTML = template.HTML(sound)
		}
		boosts, err := s.DB.Boosts(ctx, views[i].ID)
		if err != nil {
			return nil, err
		}
		views[i].Boosts = boosts
		blob, err := s.Storage.Attached(ctx, "Message", views[i].ID, "attachment")
		if err == nil {
			views[i].Attachment = &blob
			views[i].BlobURL = s.Storage.BlobURL(blob)
			views[i].DownloadURL = views[i].BlobURL + "?disposition=attachment"
			views[i].Image = storage.Variable(blob.Type())
			if views[i].Image || storage.Previewable(blob.Type()) {
				variation := storage.Resize(1200, 800, "")
				if storage.Previewable(blob.Type()) {
					variation = storage.Variation{{Key: "format", Value: storage.Symbol("webp")}, {Key: "resize_to_limit", Value: []any{int64(1200), int64(800)}}}
				}
				views[i].PreviewURL, err = s.Storage.RepresentationURL(blob, variation)
				if err != nil {
					return nil, err
				}
			}

			views[i].HTML = template.HTML(attachmentHTML(blob, views[i].BlobURL, views[i].DownloadURL, views[i].PreviewURL))
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		key := messageCacheKey(views[i].Message)
		if html, ok := s.fragments.get(key); ok {
			views[i].Fragment = html
		} else {
			body, err := s.markup("message-uncached", views[i])
			if err != nil {
				return nil, err
			}
			views[i].Fragment = s.fragments.put(key, template.HTML(body))
		}
	}
	return views, nil
}
func (s *Server) markup(name string, data any) (string, error) {
	var b bytes.Buffer
	err := s.templates.ExecuteTemplate(&b, name, data)
	return b.String(), err
}

type requestOriginKey struct{}

func messagePermalink(ctx context.Context, room, message int64) string {
	origin, _ := ctx.Value(requestOriginKey{}).(string)
	if origin == "" {
		origin = "http://example.org"
	}
	return fmt.Sprintf("%s/rooms/%d/@%d", origin, room, message)
}
func stream(action, target, markup string) string {
	if action == "remove" {
		return fmt.Sprintf(`<turbo-stream action="remove" target="%s"></turbo-stream>`, template.HTMLEscapeString(target))
	}
	attr := ""
	if action == "replace" && strings.HasPrefix(target, "presentation_message_") || action == "append" && strings.HasPrefix(target, "boosts_message_") {
		attr = ` maintain_scroll="true"`
	}
	return `<turbo-stream action="` + action + `" target="` + html.EscapeString(target) + `"` + attr + `><template>` + markup + `</template></turbo-stream>`
}
func (s *Server) publish(room int64, markup string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Cable.Publish(ctx, room, markup)
}
func writeStream(w http.ResponseWriter, markup string) {
	w.Header().Set("Content-Type", "text/vnd.turbo-stream.html; charset=utf-8")
	fmt.Fprint(w, markup)
}
func (s *Server) showMessage(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.messageItems(r.Context(), []database.Message{m})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "show-message", 200, page{User: u, Messages: views})
}
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "edit-message", 200, page{User: u, Messages: viewMessages([]database.Message{m})})
}
func (s *Server) updateMessage(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !requireMessage(w, r) {
		return
	}
	m, err = s.updateMessageAttributes(r, u, m, "message[body]", "message[attachment]", nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.messageViews(r.Context(), []database.Message{m})
	if err != nil {
		s.fail(w, err)
		return
	}
	markup, err := s.markup("presentation", views[0])
	if err != nil {
		s.fail(w, err)
		return
	}
	s.publish(m.RoomID, stream("replace", "presentation_message_"+m.ClientID, markup))
	format := respondFormat(w, r, "html", "json")
	if format == "" {
		return
	}
	if format == "json" {
		http.Error(w, "Missing template messages/show", 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/rooms/%d/messages/%d", m.RoomID, m.ID), 302)
}
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.DeleteMessage(r.Context(), u.ID, m.ID); err != nil {
		s.fail(w, err)
		return
	}
	markup := stream("remove", "message_"+m.ClientID, "")
	s.publish(m.RoomID, markup)
	if respondFormat(w, r, "turbo_stream") != "" {
		writeStream(w, markup)
	}
}
func (s *Server) boosts(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.messageViews(r.Context(), []database.Message{m})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "boosts-index", 200, page{User: u, Messages: views})
}
func (s *Server) newBoost(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "new-boost", 200, page{User: u, Messages: viewMessages([]database.Message{m})})
}
func (s *Server) createBoost(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	boost, err := s.DB.CreateBoost(r.Context(), u.ID, m.ID, r.Form.Get("boost[content]"))
	if err != nil {
		s.fail(w, err)
		return
	}
	markup, err := s.markup("boost", boost)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.publish(m.RoomID, stream("append", "boosts_message_"+m.ClientID, markup))
	http.Redirect(w, r, fmt.Sprintf("/messages/%d/boosts", m.ID), 302)
}
func (s *Server) deleteBoost(w http.ResponseWriter, r *http.Request, u database.User) {
	m, err := s.findMessage(r, u, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	id := pathInt(r, "boost")
	if err = s.DB.DeleteBoost(r.Context(), u.ID, m.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	markup := stream("remove", fmt.Sprintf("boost_%d", id), "")
	s.publish(m.RoomID, markup)
	w.WriteHeader(204)
}
func (s *Server) refreshRoom(w http.ResponseWriter, r *http.Request, u database.User) {
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	seconds, err := strconv.ParseFloat(r.URL.Query().Get("since"), 64)
	since := time.UnixMicro(int64(seconds * 1000))
	if err != nil {
		http.Error(w, "Invalid timestamp", 400)
		return
	}
	created, updated, err := s.DB.RefreshedMessages(r.Context(), room.ID, since)
	if err != nil {
		s.fail(w, err)
		return
	}
	var result strings.Builder
	for _, group := range []struct {
		action   string
		messages []database.Message
	}{{"append", created}, {"replace", updated}} {
		views, err := s.messageViews(r.Context(), group.messages)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, m := range views {
			markup, err := s.markup("message", m)
			if err != nil {
				s.fail(w, err)
				return
			}
			target := room.DOM("messages")
			if group.action == "replace" {
				target = "message_" + m.ClientID
			}
			result.WriteString(stream(group.action, target, markup))
		}
	}
	writeStream(w, result.String())
}
