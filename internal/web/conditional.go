package web

import (
	"net/http"
	"time"

	"github.com/basecamp/once-campfire-go/internal/front"
)

// File controllers keep their own emission/range policy.
func notModified(w http.ResponseWriter, r *http.Request, etag string, modified time.Time) bool {
	if !front.Fresh(r, etag, modified) {
		return false
	}
	w.Header().Del("Content-Type")
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusNotModified)
	return true
}
