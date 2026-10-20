package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func (s *Server) botRequest(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("bot_key")
	// These are single path segments. Preserve the bot boundary's rejection of
	// decoded slashes and the reserved nested-message segment before authority.
	for _, name := range []string{"room_id", "bot_key", "message_id", "id"} {
		if strings.Contains(r.PathValue(name), "/") {
			publicError(w, r, 404)
			return
		}
	}
	if key == "messages" {
		publicError(w, r, 404)
		return
	}
	var user database.User
	var err error
	fromCookie := false
	if cookie, e := r.Cookie("session_token"); e == nil {
		var token string
		if s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), s.DB.Now(), &token) == nil {
			user, err = s.DB.SessionUser(r.Context(), token)
			fromCookie = err == nil
		}
	}
	if !fromCookie {
		user, err = s.DB.Bot(r.Context(), key)
		if err != nil {
			s.requestAuthentication(w, r)
			return
		}
	} else if r.Method != "GET" && r.Method != "HEAD" && !s.browserWriteAllowed(r) {
		http.Error(w, "Invalid request origin", 422)
		return
	}
	if s.blockBrowser(w, r) {
		return
	}
	room, err := s.DB.Room(r.Context(), user.ID, roomID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var raw []byte
	if boundary := multipartBoundary(r); boundary != "" {
		r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBody)
		cleanup, e := parseMultipart(r, boundary)
		defer cleanup()
		if e != nil {
			var limit *http.MaxBytesError
			if errors.As(e, &limit) {
				http.Error(w, "Request too large", 413)
				return
			}
			http.Error(w, "Invalid upload", 400)
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Request too large", 413)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := parseRequestForm(r); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
	}
	if err := parseRequestParams(r); err != nil {
		http.Error(w, "Invalid JSON", 400)
		return
	}
	if err := normalizeScalarParams(r); err != nil {
		http.Error(w, "Invalid query", 400)
		return
	}
	attachmentSet := r.Form.Has("attachment") && !nullParam(r, "attachment")
	route, _, _ := recognizeRequest(r)
	if strings.Contains(route.Action, "::boosts::") {
		s.botBoost(w, r, user, string(raw))
		return
	}
	switch r.Method {
	case "GET", "HEAD":
		anchor, direction, linkAfter, err := messagePagination(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		result, err := s.MessageQueries.APIPage(r.Context(), s.presentationFacts(r.Context()), user.ID, room.ID, anchor, direction, linkAfter)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(result.Count))
		if result.Next != 0 {
			param := "before"
			if linkAfter {
				param = "after"
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/rooms/%d/%s/messages?%s=%d>; rel="next"`, s.origin(r), room.ID, key, param, result.Next))
		}
		writeJSON(w, 200, result.Messages)
	case "POST":
		hasUpload := r.MultipartForm != nil && len(r.MultipartForm.File["attachment"]) > 0
		if !hasUpload && strings.TrimSpace(r.Form.Get("attachment")) == "" && strings.TrimSpace(string(raw)) == "" {
			w.WriteHeader(422)
			return
		}
		var staged *storage.Staged
		var body *string
		if hasUpload {
			staged, err = s.stageAttachment(r, "attachment")
			if err != nil {
				s.fail(w, err)
				return
			}
		} else if attachmentSet {
			if r.Form.Get("attachment") != "" {
				http.Error(w, "Invalid attachment", 500)
				return
			}
		} else {
			value := strings.ToValidUTF8(string(raw), "�")
			body = &value
		}
		result, err := s.MessageCommands.Create(r.Context(), user.ID, room.ID, "", body, staged)
		if err != nil {
			s.fail(w, err)
			return
		}
		if _, err = s.MessageEffects.Created(r.Context(), s.presentationFacts(r.Context()), result); err != nil {
			s.fail(w, err)
			return
		}
		message := result.Commit.Message
		w.Header().Set("Location", fmt.Sprintf("%s/messages/%d", s.origin(r), message.ID))
		w.WriteHeader(201)
	case "PATCH", "PUT":
		message, err := s.findMessage(r, user, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		body := strings.ToValidUTF8(string(raw), "�")
		var rawBody *string = &body
		attachmentField := ""
		if attachmentSet || r.MultipartForm != nil && len(r.MultipartForm.File["attachment"]) > 0 {
			rawBody = nil
			attachmentField = "attachment"
		}
		result, err := s.updateMessageAttributes(r, user, message, "", attachmentField, rawBody)
		if err != nil {
			s.fail(w, err)
			return
		}
		message = result.Commit.Message
		if err = s.MessagePublications.Updated(r.Context(), s.presentationFacts(r.Context()), result); err != nil {
			s.fail(w, err)
			return
		}
		value, err := s.MessageQueries.APIRecord(r.Context(), s.presentationFacts(r.Context()), message)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, 200, value)
	case "DELETE":
		message, err := s.findMessage(r, user, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		result, err := s.MessageCommands.Delete(r.Context(), user.ID, message.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if _, err = s.MessagePublications.Removed(r.Context(), result); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(204)
	}
	return
}
func (s *Server) botBoost(w http.ResponseWriter, r *http.Request, u database.User, body string) {
	message, err := s.findMessage(r, u, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Method == "DELETE" {
		id := pathInt(r, "id")
		if _, err = s.BoostCommands.Delete(r.Context(), u.ID, message.ID, id); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(body) == "" {
		w.WriteHeader(422)
		return
	}
	result, err := s.BoostCommands.Create(r.Context(), u.ID, message.ID, body)
	if err != nil {
		s.fail(w, err)
		return
	}
	if result.Processing != nil {
		s.fail(w, result.Processing)
		return
	}
	boost := result.Commit
	writeJSON(w, 201, struct {
		ID        int64                `json:"id"`
		Content   string               `json:"content"`
		CreatedAt string               `json:"created_at"`
		Booster   presentation.APIUser `json:"booster"`
		Message   struct {
			ID  int64  `json:"id"`
			URL string `json:"url"`
		} `json:"message"`
	}{boost.ID, boost.Content, jsonTime(boost.CreatedAt), s.Presentation.APIUser(s.presentationFacts(r.Context()), boost.Author), struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}{boost.MessageID, fmt.Sprintf("%s/rooms/%d/messages/%d", s.origin(r), boost.RoomID, boost.MessageID)}})
}
func jsonTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(data)
}
