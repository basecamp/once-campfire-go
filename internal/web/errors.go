package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
)

// ActionDispatch::PublicExceptions serves the public page or a serialized status.
func publicError(w http.ResponseWriter, r *http.Request, status int) {
	if session, ok := w.(*sessionWriter); ok {
		if buffered, ok := session.ResponseWriter.(*responseBuffer); ok {
			buffered.exception = true
		}
	}
	for _, key := range []string{"X-Frame-Options", "X-XSS-Protection", "X-Content-Type-Options", "X-Permitted-Cross-Domain-Policies", "Referrer-Policy", "Cache-Control", "X-Version", "X-Rev"} {
		w.Header().Del(key)
	}
	formats, _ := httpcompat.Formats(formatInput(r))
	format := "html"
	if len(formats) > 0 {
		format = formats[0]
	}
	reason := http.StatusText(status)
	if status == 422 {
		reason = "Unprocessable Content"
	}
	if status == 413 {
		reason = "Content Too Large"
	}
	contentType := "text/html"
	var body []byte
	switch format {
	case "json":
		contentType = "application/json"
		body = []byte(fmt.Sprintf(`{"status":%d,"error":%q}`, status, reason))
	case "xml":
		contentType = "application/xml"
		body = []byte(fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<hash>\n  <status type=\"integer\">%d</status>\n  <error>%s</error>\n</hash>\n", status, reason))
	case "yaml":
		contentType = "application/x-yaml"
		body = []byte(fmt.Sprintf("---\n:status: %d\n:error: %s\n", status, reason))
	default:
		body, _ = fs.ReadFile(assets.Public(), strconv.Itoa(status)+".html")
	}
	if r.Method == "HEAD" {
		body = nil
		if format == "all" {
			contentType = "*/*"
		}
	}
	w.Header().Set("Content-Type", contentType+"; charset=UTF-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if len(body) > 0 {
		w.Write(body)
	}
}
