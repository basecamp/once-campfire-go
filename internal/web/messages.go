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
	"github.com/basecamp/once-campfire-go/internal/fastdb"
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

// messageViews renders messages into views for the Turbo and page paths. The
// per-view database reads (room, creator, boosts, attachment) go through the
// fastdb pool when it is available, exactly the same rows database/sql would
// return (the fastdb differential tests hold the readers to the same
// decodes); the pool slot is released when the views are done.
func (s *Server) messageViews(ctx context.Context, messages []database.Message) ([]messageView, error) {
	c, release := s.fastConnCtx(ctx)
	defer release()
	views := viewMessages(messages)
	roomNames := map[int64]string{}
	creators := map[int64]database.User{}
	for i := range views {
		name, ok := roomNames[views[i].RoomID]
		if !ok {
			room, err := s.viewRoom(c, ctx, views[i].RoomID)
			if err != nil {
				return nil, err
			}
			name = room.Name
			if room.Type == "Rooms::Direct" {
				view, err := s.displayRoom(c, ctx, room, database.User{})
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
			creator, err = s.viewUser(c, ctx, views[i].CreatorID)
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
		boosts, err := s.viewBoosts(c, ctx, views[i].ID)
		if err != nil {
			return nil, err
		}
		views[i].Boosts = boosts
		blob, err := s.viewBlob(c, ctx, "Message", views[i].ID, "attachment")
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
		key := s.fragmentKey(ctx, messageCacheKey(views[i].Message))
		if html, ok := s.fragments.get(key); ok {
			views[i].Fragment = html
		} else if s.fastRender != nil {
			body, err := s.renderMessageFragment(&views[i])
			if err != nil {
				return nil, err
			}
			views[i].Fragment = s.fragments.put(key, template.HTML(body))
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

// freshMessageView builds the view of a message this request just created
// from data already in hand — the authenticated creator, the room the
// handler already read for membership, the body, and the empty boosts and
// attachment that cannot exist for a brand-new id (ENGINE-45). It skips the
// per-view reads messageViews performs (room, creator, boosts, attachment
// blob) and is byte-identical to it for the same rows: the same fields feed
// the same fragment renderer, and nil and empty boosts range identically.
// Callers keep the messageViews path for attachment posts and direct rooms
// (whose display name needs the other member).
//
// rich, when non-nil, carries the presentation, plain text and mentions the
// create path already derived from the same body in one parse (ENGINE-45b);
// the nil case (a message with no body parameter) derives them here exactly
// as before.
func (s *Server) freshMessageView(r *http.Request, u database.User, m database.Message, room database.Room, rich *richPost) (messageView, error) {
	view := messageView{Message: m}
	view.RoomName = room.Name
	view.CreatorTitle = u.Title()
	view.CreatorUpdatedAt = u.UpdatedAt
	view.Permalink = messagePermalink(r.Context(), m.RoomID, m.ID)
	if rich != nil {
		view.HTML = template.HTML(rich.presentation)
		view.AllEmoji = allEmoji(rich.plain)
		if sound := soundHTML(rich.plain); sound != "" {
			view.HTML = template.HTML(sound)
		}
	} else {
		result, _ := richtext.Display(m.Body, s.richContext(r.Context()))
		view.HTML = template.HTML(result.Presentation)
		view.AllEmoji = allEmoji(result.Plain)
		if sound := soundHTML(result.Plain); sound != "" {
			view.HTML = template.HTML(sound)
		}
	}
	key := messageCacheKey(view.Message)
	if html, ok := s.fragments.get(key); ok {
		view.Fragment = html
	} else if s.fastRender != nil {
		body, err := s.renderMessageFragment(&view)
		if err != nil {
			return view, err
		}
		view.Fragment = s.fragments.put(key, template.HTML(body))
	} else {
		body, err := s.markup("message-uncached", view)
		if err != nil {
			return view, err
		}
		view.Fragment = s.fragments.put(key, template.HTML(body))
	}
	return view, nil
}

// viewRoom returns the room record by id, through fastdb when c is set (a
// missing room is ErrNoRows, the same sentinel database/sql returns).
func (s *Server) viewRoom(c *fastdb.Conn, ctx context.Context, id int64) (database.Room, error) {
	if c == nil {
		return s.DB.FindRoom(ctx, id)
	}
	var r fastdb.Room
	if err := c.RoomByID(&r, id); err != nil {
		return database.Room{}, err
	}
	return roomOf(r), nil
}

// viewUser returns the user record by id, through fastdb when c is set.
func (s *Server) viewUser(c *fastdb.Conn, ctx context.Context, id int64) (database.User, error) {
	if c == nil {
		return s.DB.User(ctx, id)
	}
	var u fastdb.User
	if err := c.UserByID(&u, id); err != nil {
		return database.User{}, err
	}
	return userOf(u), nil
}

// viewBoosts returns the message's boosts, through fastdb when c is set. The
// fastdb rows carry the booster name and bio raw; the title is computed here
// exactly as database.DB.Boosts computes it.
func (s *Server) viewBoosts(c *fastdb.Conn, ctx context.Context, message int64) ([]database.Boost, error) {
	if c == nil {
		return s.DB.Boosts(ctx, message)
	}
	rows, err := c.Boosts(nil, message)
	if err != nil {
		return nil, err
	}
	out := make([]database.Boost, 0, len(rows))
	for _, b := range rows {
		out = append(out, database.Boost{ID: b.ID, MessageID: b.MessageID, BoosterID: b.BoosterID, Content: b.Content, Booster: b.Booster, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt, BoosterUpdatedAt: b.BoosterUpdatedAt, BoosterTitle: (database.User{Name: b.Booster, Bio: b.Bio}).Title()})
	}
	return out, nil
}

// viewBlob returns the attachment's blob, through fastdb when c is set.
func (s *Server) viewBlob(c *fastdb.Conn, ctx context.Context, kind string, id int64, name string) (storage.Blob, error) {
	if c == nil {
		return s.Storage.Attached(ctx, kind, id, name)
	}
	var b fastdb.Blob
	if err := c.AttachedBlob(&b, kind, id, name); err != nil {
		return storage.Blob{}, err
	}
	return blobOf(b), nil
}

func (s *Server) markup(name string, data any) (string, error) {
	var b bytes.Buffer
	err := s.templates.ExecuteTemplate(&b, name, data)
	return b.String(), err
}

// renderMessageFragment renders the message-uncached fragment for v with the
// compiled renderer into a pooled buffer. The returned string is the only
// allocation: the render itself appends into the caller's buffer.
func (s *Server) renderMessageFragment(v *messageView) (string, error) {
	b := borrowBuffer()
	defer releaseBuffer(b)
	return string(s.fastRender.render(b.Bytes(), v)), nil
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
