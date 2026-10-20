package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func requireMessage(w http.ResponseWriter, r *http.Request) bool {
	for key := range r.Form {
		if strings.HasPrefix(key, "message[") {
			return true
		}
	}
	if r.MultipartForm != nil {
		for key := range r.MultipartForm.File {
			if strings.HasPrefix(key, "message[") {
				return true
			}
		}
	}
	http.Error(w, "Missing message parameter", 400)
	return false
}

// Matches MessagesController#update and Messages::ByBotsController#message_params.
// An empty field name disables that named assignment for raw-body API requests.
func (s *Server) updateMessageAttributes(r *http.Request, user database.User, message database.Message, bodyField, attachmentField string, rawBody *string) (application.MessageResult, error) {
	body := rawBody
	if body == nil && bodyField != "" && r.Form.Has(bodyField) && !nullParam(r, bodyField) {
		value := r.Form.Get(bodyField)
		body = &value
	}
	var attachment *int64
	var uploaded *storage.Staged
	if attachmentField != "" && r.MultipartForm != nil && len(r.MultipartForm.File[attachmentField]) > 0 {
		var err error
		uploaded, err = s.stageAttachment(r, attachmentField)
		if err != nil {
			return application.MessageResult{}, err
		}
	} else if attachmentField != "" && r.Form.Has(attachmentField) {
		if r.Form.Get(attachmentField) != "" {
			return application.MessageResult{}, errors.New("could not find or build blob: expected attachable")
		}
		attachment = new(int64)
	}
	return s.MessageCommands.Update(r.Context(), user.ID, message.ID, body, attachment, uploaded)
}
