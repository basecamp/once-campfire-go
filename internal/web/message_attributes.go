package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/richtext"
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
func (s *Server) updateMessageAttributes(r *http.Request, user database.User, message database.Message, bodyField, attachmentField string, rawBody *string) (database.Message, error) {
	var body *string
	plain := s.plainText(r.Context(), message.Body)
	if rawBody != nil || r.Form.Has(bodyField) && !nullParam(r, bodyField) {
		value := r.Form.Get(bodyField)
		if rawBody != nil {
			value = *rawBody
		}
		value, plain = s.canonicalMessage(r.Context(), value)
		body = &value
	}
	var attachment *int64
	var uploaded *storage.Staged
	if r.MultipartForm != nil && len(r.MultipartForm.File[attachmentField]) > 0 {
		var err error
		uploaded, err = s.stageAttachment(r, attachmentField)
		if err != nil {
			return message, err
		}
		defer uploaded.Discard()
		attachment = new(int64)
	} else if r.Form.Has(attachmentField) {
		if r.Form.Get(attachmentField) != "" {
			return message, errors.New("could not find or build blob: expected attachable")
		}
		attachment = new(int64)
	}
	if strings.TrimSpace(plain) == "" {
		if attachment != nil {
			if uploaded != nil {
				plain = storage.Filename(uploaded.Blob.Filename)
			}
		} else {
			blob, err := s.Storage.Attached(r.Context(), "Message", message.ID, "attachment")
			if err == nil {
				plain = storage.Filename(blob.Filename)
			} else if !errors.Is(err, database.ErrNoRows) {
				return message, err
			}
		}
	}
	updated, err := s.DB.UpdateMessageWithUpload(r.Context(), user.ID, message.ID, body, plain, attachment, pendingBlob(uploaded))
	if err != nil {
		return message, err
	}
	if uploaded != nil {
		s.Jobs.Enqueue("analyze", func(ctx context.Context) error {
			_, err := s.Storage.Analyze(ctx, uploaded.Blob)
			return err
		})
	}
	return updated, nil
}

func pendingBlob(staged *storage.Staged) database.BlobStager {
	if staged == nil {
		return nil
	}
	return staged
}
func (s *Server) saveNewMessage(ctx context.Context, user, room int64, client string, body *string, staged *storage.Staged, webhook bool) (database.Message, error) {
	if staged != nil {
		defer staged.Discard()
	}
	// Only the canonical body here: the search index's plain text is extracted on the writer
	// after commit, from the stored body (database.DB.PlainText), as the reference does.
	if body != nil {
		value := richtext.Canonical(*body)
		body = &value
	}
	message, err := s.DB.CreateMessageWithUpload(ctx, user, room, client, body, "", pendingBlob(staged), webhook)
	if err != nil {
		return message, err
	}
	if staged != nil {
		if _, err = s.Storage.ProcessAttachment(ctx, staged.Blob); err != nil {
			return message, err
		}
		return s.DB.Message(ctx, message.ID)
	}
	return message, nil
}
