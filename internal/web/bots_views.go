package web

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/views"
)

// Messages::ByBotsController and Messages::Boosts::ByBotsController
// (reference/crates/campfire/src/controllers/messages/{by_bots,boosts/by_bots}.rs): the bot API
// under /rooms/:room_id/:bot_key/messages, JSON by route default, rendered with the Jbuilder views
// (views/messages_json.go).

var botRoute = regexp.MustCompile(`^/rooms/([^/]+)/([^/]+)/messages(?:/([^/]+))?(?:/boosts(?:/([^/]+))?)?$`)

// botRequestViews authenticates a bot API request (the session cookie, else params[:bot_key]),
// reads its body (RawRequestBody, or a multipart attachment) and runs action. False when the
// path isn't the bot API's.
func (s *Server) botRequestViews(w http.ResponseWriter, r *http.Request, action string) bool {
	values := botRoute.FindStringSubmatch(r.URL.Path)
	if values == nil || values[2] == "messages" {
		return false
	}
	user, fromCookie, ok := s.botAuthentication(w, r)
	if !ok {
		return true
	}
	if fromCookie && r.Method != "GET" && r.Method != "HEAD" && !s.sameOrigin(r) {
		http.Error(w, "Invalid request origin", 422)
		return true
	}
	if s.blockBrowser(w, r) {
		return true
	}
	var raw []byte
	if boundary := multipartBoundary(r); boundary != "" {
		r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBody)
		cleanup, err := parseMultipart(r, boundary)
		defer cleanup()
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(w, "Request too large", 413)
			} else {
				http.Error(w, "Invalid upload", 400)
			}
			return true
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		var err error
		if raw, err = io.ReadAll(r.Body); err != nil {
			http.Error(w, "Request too large", 413)
			return true
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return true
		}
	}
	body := strings.ToValidUTF8(string(raw), "�")
	switch action {
	case "messages::by_bots::index":
		s.botMessagesIndex(w, r, &user)
	case "messages::by_bots::create":
		s.botMessagesCreate(w, r, &user, body)
	case "messages::by_bots::update":
		s.botMessagesUpdate(w, r, &user, body)
	case "messages::by_bots::destroy":
		s.botMessagesDestroy(w, r, &user)
	case "messages::boosts::by_bots::create":
		s.botBoostsCreate(w, r, &user, body)
	case "messages::boosts::by_bots::destroy":
		s.botBoostsDestroy(w, r, &user)
	default:
		return false
	}
	return true
}

// botAuthentication is require_authentication with bot access: restore_authentication (the
// session_token cookie), else bot_authentication (params[:bot_key]), else request_authentication.
func (s *Server) botAuthentication(w http.ResponseWriter, r *http.Request) (database.User, bool, bool) {
	ctx := r.Context()
	if cookie, err := r.Cookie("session_token"); err == nil {
		var token string
		if s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), s.DB.Now(), &token) == nil && token != "" {
			session, found, err := s.DB.SessionByToken(ctx, token)
			if err != nil {
				s.failWith(w, r, err)
				return database.User{}, false, false
			}
			if found {
				if session.NeedsResume(s.DB.Now()) {
					agent, ip := r.UserAgent(), remoteIP(r)
					if err = s.DB.ResumeSession(ctx, session, &agent, &ip); err == nil {
						err = s.setAuthenticationCookie(w, token)
					}
					if err != nil {
						s.failWith(w, r, err)
						return database.User{}, false, false
					}
				}
				u, found, err := s.DB.UserFindByID(ctx, session.UserID)
				if err != nil {
					s.failWith(w, r, err)
					return u, false, false
				}
				if found {
					return u, true, true
				}
			}
		}
	}
	if key, ok := param(r, "bot_key"); ok && strings.TrimSpace(key) != "" {
		bot, found, err := s.DB.UserAuthenticateBot(ctx, strings.Trim(key, " \t\n\v\f\r\x00"))
		if err != nil {
			s.failWith(w, r, err)
			return bot, false, false
		}
		if found {
			return bot, false, true
		}
	}
	s.requestAuthentication(w, r)
	return database.User{}, false, false
}

// botRoom is Messages::ByBotsController#set_room: Current.user.rooms.find_by(id: params[:room_id]),
// else head :not_found.
func (s *Server) botRoom(w http.ResponseWriter, r *http.Request, u *database.User) (database.ReferenceRoom, bool) {
	var room database.ReferenceRoom
	found := false
	if id, ok := paramID(r, "room_id"); ok {
		var err error
		if room, found, err = s.DB.RoomForUser(r.Context(), u.ID, id); err != nil {
			s.failWith(w, r, err)
			return room, false
		}
	}
	if !found {
		headFromBeforeAction(w, http.StatusNotFound)
	}
	return room, found
}

func writeJSONBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(body))
}

// botMessagesIndex is Messages::ByBotsController#index: a page of messages, with X-Total-Count and
// a Link to the next page when there is one.
func (s *Server) botMessagesIndex(w http.ResponseWriter, r *http.Request, u *database.User) {
	room, ok := s.botRoom(w, r, u)
	if !ok {
		return
	}
	messages, err := s.findPagedMessages(r, &room)
	if err == nil {
		err = s.setPaginationHeaders(w, r, &room, messages)
	}
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	if respondFormat(w, r, "json") == "" {
		return
	}
	base := s.origin(r)
	p := s.newPresenter(r.Context(), s.requestHost(r))
	result := make([]views.MessageJSON, len(messages))
	for i := range messages {
		message, err := p.messageJSON(&messages[i], base)
		if err != nil {
			s.failWith(w, r, err)
			return
		}
		result[i] = *message
	}
	writeJSONBody(w, http.StatusOK, views.MessagesByBotsIndexJSON(result))
}

// setPaginationHeaders is set_pagination_headers: X-Total-Count, and a Link to the next page.
func (s *Server) setPaginationHeaders(w http.ResponseWriter, r *http.Request, room *database.ReferenceRoom, messages []database.ReferenceMessage) error {
	after := false
	if value, ok := param(r, "after"); ok && strings.TrimSpace(value) != "" {
		after = true
	}
	ctx := r.Context()
	count, err := s.DB.MessageCountInRoom(ctx, room.ID)
	if err != nil {
		return err
	}
	key, id, next := "", int64(0), false
	if len(messages) > 0 {
		if after {
			last := messages[len(messages)-1]
			key, id = "after", last.ID
			next, err = s.DB.MessageExistsAfter(ctx, room.ID, last)
		} else {
			key, id = "before", messages[0].ID
			next, err = s.DB.MessageExistsBefore(ctx, room.ID, messages[0])
		}
		if err != nil {
			return err
		}
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(count, 10))
	if next {
		botKey, _ := param(r, "bot_key")
		url := s.origin(r) + views.RouteRoomBotMessages(room.ID, botKey) + "?" + key + "=" + strconv.FormatInt(id, 10)
		w.Header().Set("Link", "<"+url+`>; rel="next"`)
	}
	return nil
}

// botMessageParams is Messages::ByBotsController#message_params: the attachment when one is
// given, else the raw body.
func botMessageParams(r *http.Request, body string) messageParams {
	var p messageParams
	if r.MultipartForm != nil && len(r.MultipartForm.File["attachment"]) > 0 {
		p.attachmentGiven = true
		p.attachmentField = "attachment"
	} else if values, ok := r.Form["attachment"]; ok && !nullParam(r, "attachment") {
		p.attachmentGiven = true
		p.attachmentInvalid = len(values) > 0 && values[0] != ""
	} else {
		p.body = &body
	}
	return p
}

// botMessagesCreate is Messages::ByBotsController#create: 422 without a body or attachment, else
// MessagesController#create's message, broadcasts and webhooks, and 201 with its location.
func (s *Server) botMessagesCreate(w http.ResponseWriter, r *http.Request, u *database.User, body string) {
	room, ok := s.botRoom(w, r, u)
	if !ok {
		return
	}
	attachmentBlank := !(r.MultipartForm != nil && len(r.MultipartForm.File["attachment"]) > 0)
	if values, ok := r.Form["attachment"]; ok && len(values) > 0 && strings.TrimSpace(values[0]) != "" {
		attachmentBlank = false
	}
	if attachmentBlank && strings.TrimSpace(body) == "" {
		headFromBeforeAction(w, http.StatusUnprocessableEntity)
		return
	}
	message, err := s.createMessageRecord(r, u, &room, botMessageParams(r, body))
	if err == nil {
		err = s.broadcastCreate(r, &room, &message)
	}
	if err == nil {
		err = s.deliverWebhooksToBots(r.Context(), &room, &message)
	}
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	w.Header().Set("Location", s.origin(r)+views.RouteMessage(message.ID))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
}

// botMessagesUpdate is Messages::ByBotsController#update: MessagesController#update, answering
// JSON with the message.
func (s *Server) botMessagesUpdate(w http.ResponseWriter, r *http.Request, u *database.User, body string) {
	room, ok := s.botRoom(w, r, u)
	if !ok {
		return
	}
	message, err := s.setMessage(r, &room)
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	if !canAdminister(u, message.CreatorID) {
		headFromBeforeAction(w, http.StatusForbidden)
		return
	}
	params := botMessageParams(r, body)
	if params.attachmentInvalid {
		s.failWith(w, r, errors.New("could not find or build blob: expected attachable"))
		return
	}
	legacy, err := s.DB.Message(r.Context(), message.ID)
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	if _, err = s.updateMessageAttributes(r, *u, legacy, "", "attachment", params.body); err != nil {
		s.failWith(w, r, err)
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
		s.failWith(w, r, err)
		return
	}
	switch respondFormat(w, r, "html", "json") {
	case "json":
		json, err := s.newPresenter(r.Context(), s.requestHost(r)).messageJSON(&message, s.origin(r))
		if err != nil {
			s.failWith(w, r, err)
			return
		}
		writeJSONBody(w, http.StatusOK, views.MessagesByBotsShowJSON(json))
	case "html":
		s.redirectToPath(w, r, views.RouteRoomMessage(room.ID, message.ID))
	}
}

// botMessagesDestroy is Messages::ByBotsController#destroy.
func (s *Server) botMessagesDestroy(w http.ResponseWriter, r *http.Request, u *database.User) {
	room, ok := s.botRoom(w, r, u)
	if !ok {
		return
	}
	message, err := s.setMessage(r, &room)
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	if !canAdminister(u, message.CreatorID) {
		headFromBeforeAction(w, http.StatusForbidden)
		return
	}
	if err := s.DB.DeleteMessage(r.Context(), u.ID, message.ID); err != nil {
		s.failWith(w, r, err)
		return
	}
	s.publish(room.ID, turboStreamAction("remove", "message_"+message.ClientMessageID, "", false))
	w.WriteHeader(http.StatusNoContent)
}

// botBoostMessage is Messages::Boosts::ByBotsController#set_message: the room among
// Current.user.rooms, then its message; head :not_found without one.
func (s *Server) botBoostMessage(w http.ResponseWriter, r *http.Request, u *database.User) (database.ReferenceMessage, bool) {
	ctx := r.Context()
	var message database.ReferenceMessage
	found := false
	if roomID, ok := paramID(r, "room_id"); ok {
		room, roomFound, err := s.DB.RoomForUser(ctx, u.ID, roomID)
		if err != nil {
			s.failWith(w, r, err)
			return message, false
		}
		if id, ok := paramID(r, "message_id"); roomFound && ok {
			if message, found, err = s.DB.MessageFindByID(ctx, id); err != nil {
				s.failWith(w, r, err)
				return message, false
			}
			found = found && message.RoomID == room.ID
		}
	}
	if !found {
		headFromBeforeAction(w, http.StatusNotFound)
	}
	return message, found
}

// botBoostsCreate is Messages::Boosts::ByBotsController#create: the raw body as the boost's
// content, then its JSON.
func (s *Server) botBoostsCreate(w http.ResponseWriter, r *http.Request, u *database.User, body string) {
	message, ok := s.botBoostMessage(w, r, u)
	if !ok {
		return
	}
	if strings.TrimSpace(body) == "" {
		headFromBeforeAction(w, http.StatusUnprocessableEntity)
		return
	}
	boost, err := s.createBoostRecord(r, u, &message, &body)
	if err == nil {
		err = s.broadcastBoostCreate(r, &message, &boost)
	}
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	if respondFormat(w, r, "json") == "" {
		return
	}
	json, err := s.newPresenter(r.Context(), s.requestHost(r)).boostJSON(&boost, &message, s.origin(r))
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	writeJSONBody(w, http.StatusCreated, views.MessagesBoostsByBotsShowJSON(json))
}

// botBoostsDestroy is Messages::Boosts::ByBotsController#destroy: a boost that isn't the bot's is
// head :not_found.
func (s *Server) botBoostsDestroy(w http.ResponseWriter, r *http.Request, u *database.User) {
	message, ok := s.botBoostMessage(w, r, u)
	if !ok {
		return
	}
	boost, err := s.setBoost(r, u, &message)
	if err == database.ErrNoRows {
		headFromBeforeAction(w, http.StatusNotFound)
		return
	}
	if err == nil {
		err = s.destroyBoost(r, u, &message, &boost)
	}
	if err != nil {
		s.failWith(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// jbuilderKey is Jbuilder's json.cache! key, jbuilder/views/<template>:<digest>/<record key>, with
// the request's base URL, which the JSON's absolute URLs come from.
func jbuilderKey(template, table string, id int64, updatedAt time.Time, baseURL string) string {
	key := []byte("jbuilder/views/" + template + ":go/")
	key = views.AppendCacheKeyWithVersion(key, table, id, updatedAt)
	return string(append(append(key, '/'), baseURL...))
}

// cachedJSON is json.cache! key do ... end: the cached value, else compute's, kept when it succeeds.
func cachedJSON[T interface{ CacheSize() int }](cache *views.FragmentCache, key string, compute func() (T, error)) (T, error) {
	if value, ok := cache.Get(key).(T); ok {
		return value, nil
	}
	value, err := compute()
	if err != nil {
		return value, err
	}
	return cache.FetchValue(key, value.CacheSize(), func() any { return value }).(T), nil
}

// userJSON is users/_user.json.jbuilder (json.cache! user).
func (s *Server) cachedUserJSON(u *database.User, baseURL string) (*views.UserJSON, error) {
	return cachedJSON(s.views, jbuilderKey("users/_user", "users", u.ID, u.UpdatedAt, baseURL), func() (*views.UserJSON, error) {
		role := "member"
		switch u.Role {
		case 1:
			role = "administrator"
		case 2:
			role = "bot"
		}
		return &views.UserJSON{ID: u.ID, Name: u.Name, Role: role, AvatarURL: baseURL + s.avatarPath(u.ID, u.UpdatedAt)}, nil
	})
}

// messageJSON is messages/_message.json.jbuilder (json.cache! message).
func (p *presenter) messageJSON(m *database.ReferenceMessage, baseURL string) (*views.MessageJSON, error) {
	return cachedJSON(p.s.views, jbuilderKey("messages/_message", "messages", m.ID, m.UpdatedAt, baseURL), func() (*views.MessageJSON, error) {
		plain, err := p.plainTextBody(m)
		if err != nil {
			return nil, err
		}
		html, err := p.bodyHTML(m)
		if err != nil {
			return nil, err
		}
		creator, err := p.user(m.CreatorID)
		if err != nil {
			return nil, err
		}
		user, err := p.s.cachedUserJSON(&creator, baseURL)
		if err != nil {
			return nil, err
		}
		return &views.MessageJSON{ID: m.ID, CreatedAt: views.JSONTime(m.CreatedAt), Body: views.MessageBodyJSON{PlainText: plain, HTML: html},
			Creator: *user, Room: views.IDJSON{ID: m.RoomID}, URL: baseURL + views.RouteRoomMessage(m.RoomID, m.ID)}, nil
	})
}

// boostJSON is messages/boosts/_boost.json.jbuilder (json.cache! boost).
func (p *presenter) boostJSON(b *database.ReferenceBoost, message *database.ReferenceMessage, baseURL string) (*views.BoostJSON, error) {
	return cachedJSON(p.s.views, jbuilderKey("messages/boosts/_boost", "boosts", b.ID, b.UpdatedAt, baseURL), func() (*views.BoostJSON, error) {
		booster, err := p.user(b.BoosterID)
		if err != nil {
			return nil, err
		}
		user, err := p.s.cachedUserJSON(&booster, baseURL)
		if err != nil {
			return nil, err
		}
		return &views.BoostJSON{ID: b.ID, Content: b.Content, CreatedAt: views.JSONTime(b.CreatedAt), Booster: *user,
			Message: views.BoostMessageJSON{ID: b.MessageID, URL: baseURL + views.RouteRoomMessage(message.RoomID, message.ID)}}, nil
	})
}

// bodyHTML is message.body.to_s: the stored rich text rendered inside its layout ("" without one).
func (p *presenter) bodyHTML(m *database.ReferenceMessage) (string, error) {
	body, err := p.s.DB.MessageBodyHTML(p.ctx, m.ID)
	if err != nil || body == nil {
		return "", err
	}
	result, _ := richtext.Process(*body, p.richContext())
	return result.BodyHTML, nil
}
