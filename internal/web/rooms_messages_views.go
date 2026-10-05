package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/views"
	qt "github.com/valyala/quicktemplate"
)

// RoomsController#show, MessagesController, Messages::BoostsController, Rooms::RefreshesController,
// Rooms::InvolvementsController and SearchesController, rendered with the reference's views
// (reference/crates/campfire/src/controllers/{rooms,messages,searches}.rs and their modules).

const turboStreamContentType = "text/vnd.turbo-stream.html; charset=utf-8"

var (
	roomShowSize      views.RenderSize
	messagesIndexSize views.RenderSize
	searchesIndexSize views.RenderSize
)

// param is params[key]: the route's path parameters, then the query and the form.
func param(r *http.Request, key string) (string, bool) {
	if value := r.PathValue(key); value != "" {
		return value, true
	}
	if values, ok := r.Form[key]; ok && len(values) > 0 && !nullParam(r, key) {
		return values[0], true
	}
	return "", false
}

// paramID is param(key) bound to an integer column (ActiveModel::Type::Integer).
func paramID(r *http.Request, key string) (int64, bool) {
	value, ok := param(r, key)
	if !ok {
		return 0, false
	}
	return integerCast(value)
}

// redirectTo is redirect_to url_for(path): a 302 with the absolute URL and no body.
func (s *Server) redirectTo(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Location", s.origin(r)+path)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusFound)
}

// headFromBeforeAction is concerns::head: a status with text/html and no body.
func headFromBeforeAction(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status)
}

// turboStreamAction is turbo_stream_action_tag as broadcasts render it: maintain_scroll first,
// then action and target; remove carries no template.
func turboStreamAction(action, target, template string, maintainScroll bool) string {
	var b strings.Builder
	b.WriteString("<turbo-stream")
	if maintainScroll {
		b.WriteString(` maintain_scroll="true"`)
	}
	b.WriteString(` action="` + views.Escape(action) + `" target="` + views.Escape(target) + `">`)
	if action != "remove" {
		b.WriteString("<template>" + template + "</template>")
	}
	b.WriteString("</turbo-stream>")
	return b.String()
}

// setRoom is RoomsController#set_room over Current.user.rooms: params[:room_id] || params[:id],
// else back to the root with an alert.
func (s *Server) setRoom(w http.ResponseWriter, r *http.Request, u *database.User) (database.ReferenceRoom, bool) {
	value, ok := param(r, "room_id")
	if !ok {
		value, ok = param(r, "id")
	}
	var room database.ReferenceRoom
	found := false
	if id, cast := integerCast(value); ok && cast {
		var err error
		room, found, err = s.DB.RoomForUser(r.Context(), u.ID, id)
		if err != nil {
			s.fail(w, err)
			return room, false
		}
	}
	if !found {
		s.flash(r, "alert", "Room not found or inaccessible")
		s.redirectTo(w, r, views.RouteRoot())
	}
	return room, found
}

// membershipRoom is RoomScoped#set_room: Current.user.memberships.find_by!(room_id:) and its room.
// ErrNoRows (a 404) without the membership; a room that's gone fails the action.
func (s *Server) membershipRoom(r *http.Request, u *database.User) (database.ReferenceMembership, database.ReferenceRoom, error) {
	id, ok := paramID(r, "room_id")
	if !ok {
		return database.ReferenceMembership{}, database.ReferenceRoom{}, database.ErrNoRows
	}
	membership, found, err := s.DB.MembershipFindByRoomAndUser(r.Context(), id, u.ID)
	if err != nil || !found {
		if err == nil {
			err = database.ErrNoRows
		}
		return membership, database.ReferenceRoom{}, err
	}
	room, found, err := s.DB.RoomFindByID(r.Context(), membership.RoomID)
	if err == nil && !found {
		err = errors.New("the membership's room is gone")
	}
	return membership, room, err
}

// setMessage is @room.messages.find(params[:id]).
func (s *Server) setMessage(r *http.Request, room *database.ReferenceRoom) (database.ReferenceMessage, error) {
	id, ok := paramID(r, "id")
	if !ok {
		return database.ReferenceMessage{}, database.ErrNoRows
	}
	message, found, err := s.DB.MessageFindInRoom(r.Context(), room.ID, id)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	return message, err
}

// rememberLastRoomVisited is remember_last_room_visited: the last_room cookie, which the rest of
// the request reads back as cookies[:last_room] does (the layout's last_room_visited).
func rememberLastRoomVisited(s *Server, w http.ResponseWriter, r *http.Request, id int64) {
	value := strconv.FormatInt(id, 10)
	s.rememberRoom(w, r, value)
	cookies := []string{"last_room=" + value}
	for _, c := range r.Cookies() {
		if c.Name != "last_room" {
			cookies = append(cookies, c.String())
		}
	}
	r.Header.Set("Cookie", strings.Join(cookies, "; "))
}

// requireParams is params.require(key): ParameterMissing, a 400, without key's params.
func requireParams(w http.ResponseWriter, r *http.Request, key string) bool {
	for name := range r.Form {
		if strings.HasPrefix(name, key+"[") {
			return true
		}
	}
	if r.MultipartForm != nil {
		for name := range r.MultipartForm.File {
			if strings.HasPrefix(name, key+"[") {
				return true
			}
		}
	}
	publicError(w, r, http.StatusBadRequest)
	return false
}

// failWith is fail for an error the request's own format renders: the bot API's routes default
// to JSON.
func (s *Server) failWith(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, database.ErrNoRows):
		status = http.StatusNotFound
	case errors.Is(err, database.ErrForbidden):
		status = http.StatusForbidden
	default:
		slog.Error("request failed", "error", err)
	}
	publicError(w, r, status)
}

// canAdminister is Current.user.can_administer?(record): administrators and the record's creator.
func canAdminister(u *database.User, creatorID int64) bool { return u.Role == 1 || u.ID == creatorID }

// roomShow is RoomsController#show, also GET /rooms/:room_id/@:message_id: rooms/show with the page
// around params[:message_id], else the last page.
func (s *Server) roomShow(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setRoom(w, r, &u)
	if !ok {
		return
	}
	rememberLastRoomVisited(s, w, r, room.ID)
	ctx := r.Context()
	var anchor database.ReferenceMessage
	found := false
	if id, ok := paramID(r, "message_id"); ok {
		var err error
		if anchor, found, err = s.DB.MessageFindByID(ctx, id); err != nil {
			s.fail(w, err)
			return
		}
	}
	var messages []database.ReferenceMessage
	var err error
	if found && anchor.RoomID == room.ID {
		messages, err = s.DB.MessagePageAround(ctx, room.ID, anchor)
	} else {
		messages, err = s.DB.MessageLastPage(ctx, room.ID)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	p := s.newPresenter(ctx, s.requestHost(r))
	original, found, err := s.DB.FirstRoom(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	show := &views.RoomShowView{UpdatedAt: room.UpdatedAt, User: s.userView(&u),
		MessagesStreamName: s.Secrets.SignStream(rails.RoomStream(room.Type, room.ID))}
	if show.Room, err = p.roomView(&room, &u); err != nil {
		s.fail(w, err)
		return
	}
	if show.Messages, err = p.messages(messages); err != nil {
		s.fail(w, err)
		return
	}
	if found && original.ID == room.ID {
		paged, err := s.DB.MessagePaged(ctx, room.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		show.Invitation = !paged
	}
	account, found, err := s.DB.AccountFirst(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if found {
		show.JoinCode = account.JoinCode
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &roomShowSize, func(ctx *views.ViewContext) views.Page {
		return &views.RoomsShow{Ctx: ctx, Show: show}
	})
}

// findPagedMessages is find_paged_messages: the page before or after params[:before] /
// params[:after] (ErrNoRows when that message isn't in the room), else the last page.
func (s *Server) findPagedMessages(r *http.Request, room *database.ReferenceRoom) ([]database.ReferenceMessage, error) {
	ctx := r.Context()
	for _, key := range []string{"before", "after"} {
		value, ok := param(r, key)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		id, cast := integerCast(value)
		if !cast {
			return nil, database.ErrNoRows
		}
		message, found, err := s.DB.MessageFindInRoom(ctx, room.ID, id)
		if err != nil || !found {
			if err == nil {
				err = database.ErrNoRows
			}
			return nil, err
		}
		if key == "before" {
			return s.DB.MessagePageBefore(ctx, room.ID, message)
		}
		return s.DB.MessagePageAfter(ctx, room.ID, message)
	}
	return s.DB.MessageLastPage(ctx, room.ID)
}

// messagesFresh is `fresh_when @messages`: the records' cache keys digested with the frame and
// template etaggers, and their latest updated_at. True when the response is a 304.
func messagesFresh(w http.ResponseWriter, r *http.Request, messages []database.ReferenceMessage) bool {
	key := make([]byte, 0, 32*len(messages)+32)
	var modified time.Time
	for i, m := range messages {
		if i > 0 {
			key = append(key, '/')
		}
		key = views.AppendCacheKeyWithVersion(key, "messages", m.ID, m.UpdatedAt)
		if m.UpdatedAt.After(modified) {
			modified = m.UpdatedAt
		}
	}
	if r.Header.Get("Turbo-Frame") != "" {
		key = append(key, "/frame"...)
	}
	key = append(key, "/messages/index"...)
	digest := sha256.Sum256(key)
	etag := `W/"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	return notModified(w, r, etag, modified)
}

// messagesIndex is MessagesController#index (layout false): the page the client fetches while
// scrolling; 204 when it's empty.
func (s *Server) messagesIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	_, room, err := s.membershipRoom(r, &u)
	if err != nil {
		s.fail(w, err)
		return
	}
	messages, err := s.findPagedMessages(r, &room)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(messages) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if messagesFresh(w, r, messages) || respondFormat(w, r, "html") == "" {
		return
	}
	items, err := s.newPresenter(r.Context(), s.requestHost(r)).messages(messages)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.bare(w, r, &u, http.StatusOK, "html", "text/html; charset=utf-8", func(ctx *views.ViewContext) *views.RecordedPage {
		return messagesIndexSize.Render(0, func(qw *qt.Writer) { views.StreamMessagesIndex(qw, ctx, items) })
	})
}

// messageParams is params.require(:message).permit(:body, :attachment, :client_message_id): false
// (a 400) without message params.
type messageParams struct {
	body, clientMessageID *string
	// The attachment's param: message[attachment], or the bot API's attachment.
	attachmentField string
	// attachment= was given: an upload, nil or "" (removal), or something else (invalid).
	attachmentGiven, attachmentInvalid bool
}

func messageParamsOf(r *http.Request) messageParams {
	var p messageParams
	text := func(key string) *string {
		if values, ok := r.Form[key]; ok && len(values) > 0 && !nullParam(r, key) {
			return &values[0]
		}
		return nil
	}
	p.body = text("message[body]")
	p.clientMessageID = text("message[client_message_id]")
	if r.MultipartForm != nil && len(r.MultipartForm.File["message[attachment]"]) > 0 {
		p.attachmentGiven = true
	} else if values, ok := r.Form["message[attachment]"]; ok {
		p.attachmentGiven = true
		p.attachmentInvalid = len(values) > 0 && values[0] != "" && !nullParam(r, "message[attachment]")
	}
	return p
}

// createMessageRecord is @room.messages.create_with_attachment!(attributes) then process_attachment:
// with an attachment, the message is read back once analysis has touched it.
func (s *Server) createMessageRecord(r *http.Request, u *database.User, room *database.ReferenceRoom, params messageParams) (database.ReferenceMessage, error) {
	if params.attachmentInvalid {
		return database.ReferenceMessage{}, errors.New("could not find or build blob: expected attachable")
	}
	field := params.attachmentField
	if field == "" {
		field = "message[attachment]"
	}
	var staged *storage.Staged
	if params.attachmentGiven && r.MultipartForm != nil && len(r.MultipartForm.File[field]) > 0 {
		var err error
		if staged, err = s.stageAttachment(r, field); err != nil {
			return database.ReferenceMessage{}, err
		}
		defer staged.Discard()
	}
	body := params.body
	if body != nil {
		canonical := richtext.Canonical(*body)
		body = &canonical
	}
	client := ""
	if params.clientMessageID != nil {
		client = *params.clientMessageID
	}
	created, err := s.DB.CreateMessageWithUpload(r.Context(), u.ID, room.ID, client, body, "", pendingBlob(staged), false)
	if err != nil {
		return database.ReferenceMessage{}, err
	}
	message := database.ReferenceMessage{ID: created.ID, RoomID: created.RoomID, CreatorID: created.CreatorID,
		ClientMessageID: created.ClientID, CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt}
	s.enqueuePushMessage(message.ID, room)
	if staged == nil {
		return message, nil
	}
	if _, err := s.Storage.ProcessAttachment(r.Context(), staged.Blob); err != nil {
		return message, err
	}
	message, found, err := s.DB.MessageFindByID(r.Context(), message.ID)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	return message, err
}

// enqueuePushMessage is the push Room#receive enqueues after the message commits
// (Room::PushMessageJob).
func (s *Server) enqueuePushMessage(id int64, room *database.ReferenceRoom) {
	if s.Push.VAPID == nil {
		return
	}
	legacy := database.Room{ID: room.ID, CreatorID: room.CreatorID, Name: room.Name.String, Type: room.Type, UpdatedAt: room.UpdatedAt}
	s.Jobs.Enqueue("push_message", func(ctx context.Context) error { return s.pushMessage(ctx, id, legacy) })
}

// broadcastCreate is Message#broadcast_create: messages/_message, rendered without the request
// through the fragment cache, appended to the room; then each member's unread ping.
func (s *Server) broadcastCreate(r *http.Request, room *database.ReferenceRoom, message *database.ReferenceMessage) error {
	ctx := r.Context()
	view, err := s.newPresenter(ctx, "").message(message)
	if err != nil {
		return err
	}
	detached, err := s.detachedViewContext(ctx, s.rendererBaseURL(r))
	if err != nil {
		return err
	}
	html := views.CachedMessage(detached, view).HTML
	s.publish(room.ID, turboStreamAction("append", views.RoomDomID(roomKind(room.Type), room.ID, "messages"), html, false))
	memberships, err := s.DB.MembershipsForRoom(ctx, room.ID)
	if err != nil {
		return err
	}
	publish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, membership := range memberships {
		s.Cable.PublishStream(publish, fmt.Sprintf("user_%d_unreads", membership.UserID), map[string]any{"roomId": room.ID})
	}
	return nil
}

// broadcastReplace is MessagesController#update's broadcast_replace_to: messages/_presentation in
// place of [message, :presentation], keeping the scroll position.
func (s *Server) broadcastReplace(r *http.Request, room *database.ReferenceRoom, message *database.ReferenceMessage) error {
	ctx := r.Context()
	view, err := s.newPresenter(ctx, "").message(message)
	if err != nil {
		return err
	}
	detached, err := s.detachedViewContext(ctx, s.rendererBaseURL(r))
	if err != nil {
		return err
	}
	html := views.RenderString(0, func(qw *qt.Writer) { views.StreamMessagesPresentation(qw, detached, view) })
	s.publish(room.ID, turboStreamAction("replace", view.DomID("presentation"), html, true))
	return nil
}

// deliverWebhooksToBots is deliver_webhooks_to_bots: every active bot in a direct room, else
// every mentioned active bot in the room, except the message's creator; each with a webhook
// gets the message.
func (s *Server) deliverWebhooksToBots(ctx context.Context, room *database.ReferenceRoom, message *database.ReferenceMessage) error {
	var candidates []database.User
	var err error
	if room.Type == "Rooms::Direct" {
		candidates, err = s.DB.RoomActiveBots(ctx, room.ID)
	} else {
		var body *string
		if body, err = s.DB.MessageBodyHTML(ctx, message.ID); err != nil {
			return err
		}
		var ids []int64
		if body != nil {
			ids, _ = richtext.MentionIDs(*body, s.referenceRichContext(ctx, ""))
		}
		candidates, err = s.DB.MentioneesInRoom(ctx, room.ID, ids)
	}
	if err != nil {
		return err
	}
	var bots []int64
	for _, u := range candidates {
		if u.Role == 2 && u.Status == 0 && u.ID != message.CreatorID {
			bots = append(bots, u.ID)
		}
	}
	if len(bots) == 0 {
		return nil
	}
	bots, err = s.DB.BotsWithWebhooks(ctx, bots)
	if err != nil {
		return err
	}
	id := message.ID
	for _, bot := range bots {
		s.Jobs.Enqueue("webhook", func(ctx context.Context) error { return s.deliverWebhook(ctx, bot, id) })
	}
	return nil
}

// messagesCreate is MessagesController#create: the message, its broadcasts and webhooks, then
// messages/create.turbo_stream with the fragment the broadcast cached. A room that's gone renders
// messages/room_not_found.
func (s *Server) messagesCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	_, room, err := s.membershipRoom(r, &u)
	if err == database.ErrNoRows {
		if respondFormat(w, r, "html") == "" {
			return
		}
		s.contentInApplicationLayout(w, r, &u, http.StatusOK, func(*views.ViewContext) views.HTML {
			return views.HTML(views.RenderString(0, func(qw *qt.Writer) { views.StreamMessagesRoomNotFound(qw) }))
		})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if !requireParams(w, r, "message") {
		return
	}
	message, err := s.createMessageRecord(r, &u, &room, messageParamsOf(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.broadcastCreate(r, &room, &message); err != nil {
		s.fail(w, err)
		return
	}
	if err = s.deliverWebhooksToBots(r.Context(), &room, &message); err != nil {
		s.fail(w, err)
		return
	}
	if respondFormat(w, r, "turbo_stream") == "" {
		return
	}
	item, err := s.newPresenter(r.Context(), "").messageItem(&message)
	if err != nil {
		s.fail(w, err)
		return
	}
	detached, err := s.detachedViewContext(r.Context(), s.origin(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	html := views.RenderString(0, func(qw *qt.Writer) {
		views.StreamMessagesCreateTurboStream(qw, detached, item, roomKind(room.Type))
	})
	w.Header().Set("Content-Type", turboStreamContentType)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(html))
}

// messageInRoom is set_room then set_message, as the message actions run them.
func (s *Server) messageInRoom(w http.ResponseWriter, r *http.Request, u *database.User) (database.ReferenceRoom, database.ReferenceMessage, bool) {
	_, room, err := s.membershipRoom(r, u)
	if err != nil {
		s.fail(w, err)
		return room, database.ReferenceMessage{}, false
	}
	message, err := s.setMessage(r, &room)
	if err != nil {
		s.fail(w, err)
		return room, message, false
	}
	return room, message, true
}

// messagesShow is MessagesController#show: the message partial in the application layout.
func (s *Server) messagesShow(w http.ResponseWriter, r *http.Request, u database.User) {
	_, message, ok := s.messageInRoom(w, r, &u)
	if !ok || respondFormat(w, r, "html") == "" {
		return
	}
	view, err := s.newPresenter(r.Context(), s.requestHost(r)).message(&message)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.contentInApplicationLayout(w, r, &u, http.StatusOK, func(ctx *views.ViewContext) views.HTML {
		return views.HTML(views.RenderString(0, func(qw *qt.Writer) { views.StreamMessagesShow(qw, ctx, view) }))
	})
}

// messagesEdit is MessagesController#edit.
func (s *Server) messagesEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	_, message, ok := s.messageInRoom(w, r, &u)
	if !ok {
		return
	}
	if !canAdminister(&u, message.CreatorID) {
		headFromBeforeAction(w, http.StatusForbidden)
		return
	}
	if respondFormat(w, r, "html") == "" {
		return
	}
	p := s.newPresenter(r.Context(), s.requestHost(r))
	editable, err := p.editableBody(&message)
	if err != nil {
		s.fail(w, fmt.Errorf("editable_body raised: %w", err))
		return
	}
	view, err := p.message(&message)
	if err != nil {
		s.fail(w, err)
		return
	}
	edit := &views.MessageEditView{Message: *view, EditableBodyHTML: editable}
	s.contentInApplicationLayout(w, r, &u, http.StatusOK, func(ctx *views.ViewContext) views.HTML {
		return views.HTML(views.RenderString(0, func(qw *qt.Writer) { views.StreamMessagesEdit(qw, ctx, edit) }))
	})
}

// messagesUpdate is MessagesController#update: the body or attachment, the presentation
// broadcast, then back to the message (JSON has no template).
func (s *Server) messagesUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	room, message, ok := s.messageInRoom(w, r, &u)
	if !ok {
		return
	}
	if !canAdminister(&u, message.CreatorID) {
		headFromBeforeAction(w, http.StatusForbidden)
		return
	}
	if !requireParams(w, r, "message") {
		return
	}
	legacy, err := s.DB.Message(r.Context(), message.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err = s.updateMessageAttributes(r, u, legacy, "message[body]", "message[attachment]", nil); err != nil {
		s.fail(w, err)
		return
	}
	message, found, err := s.DB.MessageFindByID(r.Context(), message.ID)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	if err == nil {
		err = s.broadcastReplace(r, &room, &message)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	switch respondFormat(w, r, "html", "json") {
	case "json":
		s.fail(w, errors.New("Missing template messages/show"))
	case "html":
		s.redirectTo(w, r, views.RouteRoomMessage(room.ID, message.ID))
	}
}

// messagesDestroy is MessagesController#destroy: the message goes, broadcast_remove, then
// messages/destroy.turbo_stream.
func (s *Server) messagesDestroy(w http.ResponseWriter, r *http.Request, u database.User) {
	room, message, ok := s.messageInRoom(w, r, &u)
	if !ok {
		return
	}
	if !canAdminister(&u, message.CreatorID) {
		headFromBeforeAction(w, http.StatusForbidden)
		return
	}
	if err := s.DB.DeleteMessage(r.Context(), u.ID, message.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.publish(room.ID, turboStreamAction("remove", "message_"+message.ClientMessageID, "", false))
	if respondFormat(w, r, "turbo_stream") == "" {
		return
	}
	view, err := s.newPresenter(r.Context(), s.requestHost(r)).message(&message)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.bare(w, r, &u, http.StatusOK, "turbo_stream", turboStreamContentType, func(*views.ViewContext) *views.RecordedPage {
		return views.Render(0, func(qw *qt.Writer) { views.StreamMessagesDestroyTurboStream(qw, view) })
	})
}

// reachableMessage is Messages::BoostsController#set_message:
// Current.user.reachable_messages.find(params[:message_id]).
func (s *Server) reachableMessage(w http.ResponseWriter, r *http.Request, u *database.User) (database.ReferenceMessage, bool) {
	id, ok := paramID(r, "message_id")
	var message database.ReferenceMessage
	found := false
	if ok {
		var err error
		if message, found, err = s.DB.MessageFindReachable(r.Context(), u.ID, id); err != nil {
			s.fail(w, err)
			return message, false
		}
	}
	if !found {
		s.fail(w, database.ErrNoRows)
	}
	return message, found
}

// boostsIndex is Messages::BoostsController#index.
func (s *Server) boostsIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	message, ok := s.reachableMessage(w, r, &u)
	if !ok || respondFormat(w, r, "html") == "" {
		return
	}
	view, err := s.newPresenter(r.Context(), s.requestHost(r)).message(&message)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.content(w, r, &u, http.StatusOK, func(ctx *views.ViewContext) views.HTML {
		return views.HTML(views.RenderString(0, func(qw *qt.Writer) { views.StreamMessagesBoostsIndex(qw, ctx, view) }))
	})
}

// boostsNew is Messages::BoostsController#new.
func (s *Server) boostsNew(w http.ResponseWriter, r *http.Request, u database.User) {
	message, ok := s.reachableMessage(w, r, &u)
	if !ok || respondFormat(w, r, "html") == "" {
		return
	}
	user := s.userView(&u)
	view, err := s.newPresenter(r.Context(), s.requestHost(r)).message(&message)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.content(w, r, &u, http.StatusOK, func(ctx *views.ViewContext) views.HTML {
		return views.HTML(views.RenderString(0, func(qw *qt.Writer) { views.StreamMessagesBoostsNew(qw, ctx, view, &user) }))
	})
}

// boostsCreate is Messages::BoostsController#create: the boost, broadcast_create, then back to the
// message's boosts.
func (s *Server) boostsCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	message, ok := s.reachableMessage(w, r, &u)
	if !ok {
		return
	}
	content, ok := requireBoostContent(w, r)
	if !ok {
		return
	}
	boost, err := s.createBoostRecord(r, &u, &message, content)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.broadcastBoostCreate(r, &message, &boost); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectTo(w, r, views.RouteMessageBoosts(message.ID))
}

// requireBoostContent is params.require(:boost).permit(:content): a 400 without boost params.
func requireBoostContent(w http.ResponseWriter, r *http.Request) (*string, bool) {
	if !requireParams(w, r, "boost") {
		return nil, false
	}
	if values, ok := r.Form["boost[content]"]; ok && len(values) > 0 && !nullParam(r, "boost[content]") {
		return &values[0], true
	}
	return nil, true
}

// createBoostRecord is @message.boosts.create!(content:) by Current.user: a nil content violates the
// column's NOT NULL.
func (s *Server) createBoostRecord(r *http.Request, u *database.User, message *database.ReferenceMessage, content *string) (database.ReferenceBoost, error) {
	if content == nil {
		return database.ReferenceBoost{}, errors.New("NOT NULL constraint failed: boosts.content")
	}
	boost, err := s.DB.CreateBoost(r.Context(), u.ID, message.ID, *content)
	return database.ReferenceBoost{ID: boost.ID, MessageID: boost.MessageID, BoosterID: boost.BoosterID, Content: boost.Content,
		CreatedAt: boost.CreatedAt, UpdatedAt: boost.UpdatedAt}, err
}

// broadcastBoostCreate is Messages::BoostsController#broadcast_create: messages/boosts/_boost
// appended to the message's boosts.
func (s *Server) broadcastBoostCreate(r *http.Request, message *database.ReferenceMessage, boost *database.ReferenceBoost) error {
	ctx := r.Context()
	view, err := s.newPresenter(ctx, "").boost(boost)
	if err != nil {
		return err
	}
	detached, err := s.detachedViewContext(ctx, s.rendererBaseURL(r))
	if err != nil {
		return err
	}
	html := views.CachedBoost(detached, &view).HTML
	room, found, err := s.DB.RoomFindByID(ctx, message.RoomID)
	if err != nil || !found {
		if err == nil {
			err = database.ErrNoRows
		}
		return err
	}
	s.publish(room.ID, turboStreamAction("append", "boosts_message_"+message.ClientMessageID, html, true))
	return nil
}

// boostsDestroy is Messages::BoostsController#destroy: Current.user's boost goes, broadcast_remove,
// then 204.
func (s *Server) boostsDestroy(w http.ResponseWriter, r *http.Request, u database.User) {
	message, ok := s.reachableMessage(w, r, &u)
	if !ok {
		return
	}
	boost, err := s.setBoost(r, &u, &message)
	if err == nil {
		err = s.destroyBoost(r, &u, &message, &boost)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setBoost is @message.boosts.find_by!(id: params[:id], booster: Current.user).
func (s *Server) setBoost(r *http.Request, u *database.User, message *database.ReferenceMessage) (database.ReferenceBoost, error) {
	id, ok := paramID(r, "id")
	if !ok {
		return database.ReferenceBoost{}, database.ErrNoRows
	}
	boost, found, err := s.DB.BoostFindByMessageAndBooster(r.Context(), message.ID, id, u.ID)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	return boost, err
}

// destroyBoost is @boost.destroy! then broadcast_remove.
func (s *Server) destroyBoost(r *http.Request, u *database.User, message *database.ReferenceMessage, boost *database.ReferenceBoost) error {
	if err := s.DB.DeleteBoost(r.Context(), u.ID, message.ID, boost.ID); err != nil {
		return err
	}
	room, found, err := s.DB.RoomFindByID(r.Context(), message.RoomID)
	if err != nil || !found {
		if err == nil {
			err = database.ErrNoRows
		}
		return err
	}
	s.publish(room.ID, turboStreamAction("remove", "boost_"+strconv.FormatInt(boost.ID, 10), "", false))
	return nil
}

// lastUpdatedAt is Rooms::RefreshesController#set_last_updated_at: Time.at(0, params[:since].to_i,
// :millisecond), held to the representable range.
func lastUpdatedAt(r *http.Request) string {
	var since int64
	if value, ok := param(r, "since"); ok {
		var cast bool
		if since, cast = rubyToI(value); !cast {
			since = math.MaxInt64
			if strings.HasPrefix(strings.TrimLeft(value, " \t\n\v\f\r"), "-") {
				since = math.MinInt64
			}
		}
	}
	const minMS, maxMS = -377705116800000, 253402300799999
	since = min(max(since, minMS), maxMS)
	return database.ToDB(time.UnixMilli(since))
}

// refreshShow is Rooms::RefreshesController#show: the messages created and updated since the
// client last loaded the room.
func (s *Server) refreshShow(w http.ResponseWriter, r *http.Request, u database.User) {
	_, room, err := s.membershipRoom(r, &u)
	if err != nil {
		s.fail(w, err)
		return
	}
	since := lastUpdatedAt(r)
	if respondFormat(w, r, "turbo_stream") == "" {
		return
	}
	ctx := r.Context()
	created, err := s.DB.MessagePageCreatedSince(ctx, room.ID, since)
	if err != nil {
		s.fail(w, err)
		return
	}
	ids := make([]int64, len(created))
	for i, m := range created {
		ids[i] = m.ID
	}
	updated, err := s.DB.MessagePageUpdatedSince(ctx, room.ID, since, ids)
	if err != nil {
		s.fail(w, err)
		return
	}
	p := s.newPresenter(ctx, s.requestHost(r))
	refresh := &views.RefreshView{RoomID: room.ID, RoomKind: roomKind(room.Type)}
	if refresh.NewMessages, err = p.messages(created); err == nil {
		refresh.UpdatedMessages, err = p.messages(updated)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.bare(w, r, &u, http.StatusOK, "turbo_stream", turboStreamContentType, func(ctx *views.ViewContext) *views.RecordedPage {
		return views.Render(0, func(qw *qt.Writer) { views.StreamRoomsRefreshesShowTurboStream(qw, ctx, refresh) })
	})
}

// involvementShow is Rooms::InvolvementsController#show.
func (s *Server) involvementShow(w http.ResponseWriter, r *http.Request, u database.User) {
	membership, room, err := s.membershipRoom(r, &u)
	if err != nil {
		s.fail(w, err)
		return
	}
	if membership.Involvement == nil {
		// button_to_change_involvement's image_tag("notification-bell-.svg") raises: no such asset.
		s.fail(w, errors.New("the asset notification-bell-.svg is not present in the asset pipeline"))
		return
	}
	involvement := &views.InvolvementView{RoomID: room.ID, Kind: roomKind(room.Type), Involvement: *membership.Involvement}
	s.content(w, r, &u, http.StatusOK, func(ctx *views.ViewContext) views.HTML {
		return views.HTML(views.RenderString(0, func(qw *qt.Writer) { views.StreamRoomsInvolvementsShow(qw, ctx, involvement) }))
	})
}

// involvementParam is params[:involvement] as the enum casts it: blank is nil, anything that
// isn't an involvement raises.
func involvementParam(r *http.Request) (*string, error) {
	value, ok := param(r, "involvement")
	if !ok || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	switch value {
	case "invisible", "nothing", "mentions", "everything":
		return &value, nil
	}
	return nil, fmt.Errorf("%q is not a valid involvement", value)
}

// involvementUpdate is Rooms::InvolvementsController#update, with broadcast_visibility_changes.
func (s *Server) involvementUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	membership, room, err := s.membershipRoom(r, &u)
	if err != nil {
		s.fail(w, err)
		return
	}
	involvement, err := involvementParam(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	previous := membership.Involvement
	if err = s.DB.UpdateMembershipInvolvement(r.Context(), membership, involvement); err != nil {
		s.fail(w, err)
		return
	}
	// render_shared_room: users/sidebars/rooms/_shared, rendered without the request (which reads
	// the account first).
	if _, _, err = s.DB.AccountFirst(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	shared := &views.SidebarRoom{ID: room.ID, ParamKey: roomKind(room.Type).ParamKey(), Name: room.Name.String}
	html := views.RenderString(0, func(qw *qt.Writer) { views.StreamUsersSidebarsRoomsShared(qw, shared) })
	if room.Type != "Rooms::Direct" {
		stream := rails.UserRoomsStream(u.ID)
		publish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		switch {
		case involvement != nil && *involvement == "invisible":
			s.Cable.PublishStream(publish, stream, turboStreamAction("remove", views.RoomDomID(roomKind(room.Type), room.ID, "list"), "", false))
		case previous == nil:
			s.fail(w, errors.New("undefined method 'inquiry' for nil"))
			return
		case *previous == "invisible":
			s.Cable.PublishStream(publish, stream, turboStreamAction("prepend", "shared_rooms", html, false))
		}
	}
	s.redirectTo(w, r, views.RouteRoomInvolvement(room.ID))
}

// searchQueryParam is params[:q] (nil when absent or null).
func searchQueryParam(r *http.Request) *string {
	if values, ok := r.Form["q"]; ok && len(values) > 0 && !nullParam(r, "q") {
		return &values[0]
	}
	return nil
}

// searchQuery is params[:q]&.gsub(/[^[:word:]]/, " ").
func searchQuery(q *string) *string {
	if q == nil {
		return nil
	}
	query := database.SearchQuery(*q)
	return &query
}

// searchableQuery is the query set_messages searches for: none when it's blank.
func searchableQuery(q *string) *string {
	query := searchQuery(q)
	if query == nil || strings.TrimSpace(*query) == "" {
		return nil
	}
	return query
}

// searchesIndex is SearchesController#index: the results, the recent searches and the way back to
// the last room.
func (s *Server) searchesIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	ctx := r.Context()
	q := searchQueryParam(r)
	query := searchableQuery(q)
	var messages []database.ReferenceMessage
	var err error
	if query != nil {
		if messages, err = s.DB.MessageSearchReachable(ctx, u.ID, *query); err != nil {
			s.fail(w, err)
			return
		}
	}
	recent, err := s.DB.SearchQueriesOrderedForUser(ctx, u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	lastRoom, found, err := s.lastRoomVisitedIn(ctx, u.ID, lastRoomCookie(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	index := &views.SearchIndexView{Query: query, Q: q, RecentSearches: recent}
	if found {
		index.ReturnToRoomID = lastRoom.ID
	}
	if index.Messages, err = s.newPresenter(ctx, s.requestHost(r)).messages(messages); err != nil {
		s.fail(w, err)
		return
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &searchesIndexSize, func(ctx *views.ViewContext) views.Page {
		return &views.SearchesIndex{Ctx: ctx, Index: index}
	})
}

// searchMessages is set_messages, which runs before every searches action.
func (s *Server) searchMessages(r *http.Request, u *database.User, q *string) error {
	query := searchableQuery(q)
	if query == nil {
		return nil
	}
	_, err := s.DB.MessageSearchReachable(r.Context(), u.ID, *query)
	return err
}

// searchesCreate is SearchesController#create: Current.user.searches.record(query), then its
// results.
func (s *Server) searchesCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	q := searchQueryParam(r)
	if err := s.searchMessages(r, &u, q); err != nil {
		s.fail(w, err)
		return
	}
	query := searchQuery(q)
	if query == nil {
		s.fail(w, errors.New("NOT NULL constraint failed: searches.query"))
		return
	}
	if err := s.DB.SearchRecord(r.Context(), u.ID, *query); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectTo(w, r, views.SearchPath(*query))
}

// searchesClear is SearchesController#clear.
func (s *Server) searchesClear(w http.ResponseWriter, r *http.Request, u database.User) {
	if err := s.searchMessages(r, &u, searchQueryParam(r)); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.DB.SearchDestroyAllForUser(r.Context(), u.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectTo(w, r, views.RouteSearches())
}
