package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/views"
)

var responseBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func borrowBuffer() *bytes.Buffer { return responseBuffers.Get().(*bytes.Buffer) }
func releaseBuffer(b *bytes.Buffer) {
	if b.Cap() <= 1<<20 {
		b.Reset()
		responseBuffers.Put(b)
	}
}

// Rack::ETag and Rack::ConditionalGet operate on completed, non-streaming bodies.
// Disk/representation downloads and upgraded sockets retain their streaming writers.
type responseBuffer struct {
	http.ResponseWriter
	body      *bytes.Buffer
	status    int
	exception bool
	parts     [][]byte
	// page is a recorded page (writePage): its ETag comes from its parts.
	page *views.RecordedPage
}

func (w *responseBuffer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *responseBuffer) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body == nil {
		w.body = borrowBuffer()
	}
	return w.body.Write(body)
}
func (w *responseBuffer) finish(r *http.Request) {
	if w.body == nil {
		w.body = borrowBuffer()
	}
	defer releaseBuffer(w.body)
	if w.page != nil {
		defer views.ReleasePage(w.page)
	}
	if w.status == 0 {
		w.status = 200
	}
	h := w.Header()
	digested := false
	if !w.exception && (w.status == 200 || w.status == 201) && h.Get("ETag") == "" && h.Get("Last-Modified") == "" {
		if w.page != nil {
			if etag := w.page.ETag(); etag != "" {
				h.Set("ETag", `W/"`+etag+`"`)
				digested = true
			}
		} else if w.body.Len() > 0 {
			hash := sha256.Sum256(w.body.Bytes())
			h.Set("ETag", fmt.Sprintf("W/\"%x\"", hash[:16]))
			digested = true
		}
	}
	if !w.exception && h.Get("Cache-Control") == "" {
		value := "no-cache"
		if digested {
			value = "max-age=0, private, must-revalidate"
		}
		h.Set("Cache-Control", value)
	}
	if w.status == 200 {
		modified, _ := http.ParseTime(h.Get("Last-Modified"))
		if notModified(w.ResponseWriter, r, h.Get("ETag"), modified) {
			return
		}
	}
	if h.Get("Content-Type") == "" && w.status != 204 && w.status != 304 {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	if w.page != nil && w.status != 204 && w.status != 304 {
		// One write of the assembled page: net/http has no vectored writes, so the page's
		// parts are copied into one buffer rather than written one by one.
		w.body.Reset()
		w.body.Grow(w.page.Len())
		w.page.WriteTo(w.body)
	}
	if len(w.parts) > 0 && w.status != 204 && w.status != 304 {
		size := 0
		for _, part := range w.parts {
			size += len(part)
		}
		h.Set("Content-Length", strconv.Itoa(size))
	} else if w.status != 204 && w.status != 304 && h.Get("Content-Length") == "" {
		h.Set("Content-Length", strconv.Itoa(w.body.Len()))
	}
	w.ResponseWriter.WriteHeader(w.status)
	if r.Method != "HEAD" && w.status != 204 && w.status != 304 {
		if len(w.parts) > 0 {
			for _, part := range w.parts {
				w.ResponseWriter.Write(part)
			}
		} else {
			w.ResponseWriter.Write(w.body.Bytes())
		}
	}
}
