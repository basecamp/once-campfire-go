package web

import (
	"bytes"
	"fmt"

	"net/http"
	"strconv"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
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
	parts     []responsebody.Part
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
	if w.status == 0 {
		w.status = 200
	}
	parts := w.parts
	if len(parts) == 0 {
		// The buffer is not recycled until all writes below have completed.
		parts = []responsebody.Part{responsebody.NewPart(w.body.Bytes())}
	}
	h := w.Header()
	digested := false
	if !w.exception && (w.status == 200 || w.status == 201) && w.body.Len() > 0 && h.Get("ETag") == "" && h.Get("Last-Modified") == "" {
		hash := parts[0].Digest()
		h.Set("ETag", fmt.Sprintf("W/\"%x\"", hash[:16]))
		digested = true
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
	if len(w.parts) > 0 && w.status != 204 && w.status != 304 {
		size := 0
		for _, part := range w.parts {
			size += part.Len()
		}
		h.Set("Content-Length", strconv.Itoa(size))
	}
	w.ResponseWriter.WriteHeader(w.status)
	if r.Method != "HEAD" && w.status != 204 && w.status != 304 {
		// Hand completed bytes to the compressor without joining recorded parts.
		// Streaming/download writers do not use this optional contract.
		if writer, ok := w.ResponseWriter.(interface {
			WriteBody([]responsebody.Part) (int, error)
		}); ok {
			writer.WriteBody(parts)
		} else {
			for _, part := range parts {
				part.WriteTo(w.ResponseWriter)
			}
		}
	}
}
