package web

import (
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/httpcompat"
)

func formatInput(r *http.Request) httpcompat.FormatInput {
	var format *string
	if value := r.PathValue("format"); value != "" {
		format = &value
	} else if r.Form.Has("format") {
		value := r.Form.Get("format")
		format = &value
	}
	return httpcompat.FormatInput{Format: format, Accept: r.Header.Get("Accept"), ContentType: r.Header.Get("Content-Type"), Path: r.URL.Path, XHR: r.Header.Get("X-Requested-With") == "XMLHttpRequest"}
}
func respondFormat(w http.ResponseWriter, r *http.Request, available ...string) string {
	input := formatInput(r)
	format, err := httpcompat.Negotiate(input, available...)
	// ActionController::UnknownFormat, and an invalid Accept header (Mime::Type::InvalidMimeType),
	// are a 406 from the public exceptions app.
	if err != nil || format == "" {
		publicError(w, r, http.StatusNotAcceptable)
		return ""
	}
	if input.UsesAccept() {
		vary := w.Header().Get("Vary")
		if !strings.Contains(strings.ToLower(vary), "accept") {
			if vary != "" {
				vary += ", "
			}
			w.Header().Set("Vary", vary+"Accept")
		}
	}
	return format
}
