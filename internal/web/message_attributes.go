package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

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
func (s *Server) updateMessageAttributes(r *http.Request, user database.User, message database.Message, bodyField, attachmentField string, rawBody *string) (database.Message, error) {
	var body *string
	var plain string
	if rawBody != nil || r.Form.Has(bodyField) && !nullParam(r, bodyField) {
		value := r.Form.Get(bodyField)
		if rawBody != nil {
			value = *rawBody
		}
		value, plain = s.canonicalMessage(r.Context(), value)
		body = &value
	} else {
		plain = s.plainText(r.Context(), message.Body)
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
			} else if !errors.Is(err, sql.ErrNoRows) {
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
	return s.saveNewMessageRich(ctx, user, room, client, body, staged, webhook, nil)
}

// saveNewMessageRich is saveNewMessage with the request's single-parse rich
// result (ENGINE-45b): when rich is non-nil its canonical body and plain text
// are stored directly — they are canonicalMessage's outputs for the same
// input — so the body is not parsed a second time. Callers without a
// precomputed result pass nil and get the original canonicalize-then-store
// shape.
func (s *Server) saveNewMessageRich(ctx context.Context, user, room int64, client string, body *string, staged *storage.Staged, webhook bool, rich *richPost) (database.Message, error) {
	if staged != nil {
		defer staged.Discard()
	}
	plain := ""
	if body != nil {
		if rich != nil {
			body = &rich.canonical
			plain = rich.plain
		} else {
			value, text := s.canonicalMessage(ctx, *body)
			body = &value
			plain = text
		}
	}
	if strings.TrimSpace(plain) == "" && staged != nil {
		plain = storage.Filename(staged.Blob.Filename)
	}
	message, err := s.DB.CreateMessageWithUpload(ctx, user, room, client, body, plain, pendingBlob(staged), webhook)
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
