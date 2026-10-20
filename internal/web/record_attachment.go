package web

import (
	"errors"
	"net/http"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// Mirrors the pinned Rust assignment: omitted, deleted, uploaded, or invalid.
// Profile params.compact drops nil; account and bot params retain it.
func recordAttachment(r *http.Request, field string, upload *storage.Staged, compact bool) (application.Attachment, error) {
	if upload != nil {
		return application.Attachment{File: upload}, nil
	}
	if !r.Form.Has(field) || compact && nullParam(r, field) {
		return application.Attachment{}, nil
	}
	if r.Form.Get(field) != "" {
		return application.Attachment{}, errors.New("could not find or build blob: expected attachable")
	}
	return application.Attachment{Assigned: true}, nil
}
