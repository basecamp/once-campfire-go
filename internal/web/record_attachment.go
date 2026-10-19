package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

// Mirrors the pinned Rust attachment Assignment: omitted, deleted, uploaded, or
// invalid. Profile params.compact drops nil; account and bot params retain it.
func recordAttachment(r *http.Request, field string, upload *storage.Staged, compact bool) database.BlobStager {
	if upload != nil {
		return upload
	}
	if !r.Form.Has(field) || compact && nullParam(r, field) {
		return nil
	}
	return attachmentAssignment{invalid: r.Form.Get(field) != ""}
}

type attachmentAssignment struct{ invalid bool }

func (a attachmentAssignment) Insert(context.Context, database.UploadTx) (int64, error) {
	if a.invalid {
		return 0, errors.New("could not find or build blob: expected attachable")
	}
	return 0, nil
}
func (attachmentAssignment) Keep()    {}
func (attachmentAssignment) Discard() {}
