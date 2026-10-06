package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"net/http"
	"strconv"
	"sync"
	"time"
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
	// encoded is the single assembled recorded-response body (piece path). It
	// is sized once from the pieces' total and written in one call; parts
	// remains the legacy multi-part path.
	encoded []byte
	// encodedLength carries the body length when there are no bytes to write
	// (a HEAD request on the piece path): assembly is skipped and only the
	// Content-Length the GET response would have is reported.
	encodedLength int
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
	h := w.Header()
	digested := false
	if !w.exception && (w.status == 200 || w.status == 201) && w.body.Len() > 0 && h.Get("ETag") == "" && h.Get("Last-Modified") == "" {
		hash := sha256.Sum256(w.body.Bytes())
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
		// http.ParseTime allocates on every call, including the empty value
		// most recorded responses carry, so parse only a present header.
		modified := time.Time{}
		if value := h.Get("Last-Modified"); value != "" {
			if parsed, err := http.ParseTime(value); err == nil {
				modified = parsed
			}
		}
		if notModified(w.ResponseWriter, r, h.Get("ETag"), modified) {
			return
		}
	}
	if h.Get("Content-Type") == "" && w.status != 204 && w.status != 304 {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	// Content-Length is formatted into a stack buffer; the one string that
	// reaches the header map is the allocation, not the integer conversion.
	var length [20]byte
	if w.status != 204 && w.status != 304 {
		switch {
		case len(w.encoded) > 0:
			h.Set("Content-Length", string(strconv.AppendInt(length[:0], int64(len(w.encoded)), 10)))
		case w.encodedLength > 0:
			h.Set("Content-Length", string(strconv.AppendInt(length[:0], int64(w.encodedLength), 10)))
		case len(w.parts) > 0:
			size := 0
			for _, part := range w.parts {
				size += len(part)
			}
			h.Set("Content-Length", string(strconv.AppendInt(length[:0], int64(size), 10)))
		}
	}
	w.ResponseWriter.WriteHeader(w.status)
	if r.Method != "HEAD" && w.status != 204 && w.status != 304 {
		if len(w.encoded) > 0 {
			w.ResponseWriter.Write(w.encoded)
		} else if len(w.parts) > 0 {
			for _, part := range w.parts {
				w.ResponseWriter.Write(part)
			}
		} else {
			w.ResponseWriter.Write(w.body.Bytes())
		}
	}
}
