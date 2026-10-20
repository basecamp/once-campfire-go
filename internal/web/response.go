package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/front"
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

// Finite output is prepared before emission. A successful file selection switches
// to its own streaming policy; failures before selecting a file remain completed
// application responses. Cookies still commit through the session writer.
type responseBuffer struct {
	http.ResponseWriter
	body      *bytes.Buffer
	status    int
	exception bool
	streaming bool
	parts     []responsebody.Part
	server    *Server
}

func (w *responseBuffer) WriteHeader(status int) {
	if w.streaming {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
	}
}
func (w *responseBuffer) Write(body []byte) (int, error) {
	if w.streaming {
		return w.ResponseWriter.Write(body)
	}
	if w.status == 0 {
		w.status = 200
	}
	if w.body == nil {
		w.body = borrowBuffer()
	}
	return w.body.Write(body)
}

func streamFileResponse(w http.ResponseWriter) {
	for {
		if buffered, ok := w.(*responseBuffer); ok {
			if buffered.status != 0 || buffered.body != nil || len(buffered.parts) != 0 {
				panic("file selected after completed response output")
			}
			buffered.streaming = true
			return
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = wrapper.Unwrap()
	}
}

func (w *responseBuffer) finish(r *http.Request) {
	if w.streaming {
		return
	}
	parts := w.parts
	if len(parts) == 0 {
		if w.body == nil {
			w.body = borrowBuffer()
		}
		// This buffer remains borrowed until synchronous emission finishes.
		parts = []responsebody.Part{responsebody.NewPart(w.body.Bytes())}
	}
	if w.body != nil {
		defer releaseBuffer(w.body)
	}
	alreadyEncoded := w.Header().Get("Content-Encoding") != ""
	response, err := front.PrepareResponse(w.ResponseWriter, r, front.CompletedResponse{Status: w.status, Parts: parts, Exception: w.exception})
	if err != nil {
		w.Header().Del("Content-Encoding")
		w.Header().Del("Content-Length")
		w.Header().Del("ETag")
		http.Error(w.ResponseWriter, "Internal server error", http.StatusInternalServerError)
	} else {
		if w.server != nil && !alreadyEncoded {
			response = w.server.cacheResponse(r, w, response)
		}
		err = front.EmitResponse(w.ResponseWriter, r, response)
	}
	if err != nil && r.Context().Err() == nil {
		slog.Debug("response delivery failed", "error", err)
	}
}
