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
	// partsBuf backs parts for the recorded identity path (at most three shell
	// segments plus two slots), so the piece raws need no per-request slice.
	partsBuf [5][]byte
	// encoded is the single assembled recorded-response body (piece path). It
	// is sized once from the pieces' total and written in one call; parts
	// remains the legacy multi-part path. assembly owns the pooled buffer
	// behind encoded and is released by finish; when the arena path carved
	// the body instead, assembly is nil and the arena owns it.
	encoded  []byte
	assembly *recordedAssemblyBuffer
	// encodedLength carries the body length when there are no bytes to write
	// (a HEAD request on the piece path): assembly is skipped and only the
	// Content-Length the GET response would have is reported.
	encodedLength int
	// arena is the per-request block the recorded path carves its head
	// scratch and body from; nil when the arena path is off.
	arena *requestArena
	// framed is the writer-side precomposed gate: the response buffer sits on
	// a writer chain that ends in a precomposedReceiver (fastserve) and the
	// precomposed/arena flags are on. When framed, the fixed security and
	// recorded headers skip the http.Header map entirely (they live in the
	// head block); a request that later falls back to the map path restores
	// them via restoreRecordedHeaders.
	framed bool
	// recordedHead is the precomposed ENGINE-49 head block (arena memory),
	// emitted verbatim by the owned writer at finish; precomposed marks the
	// path taken, recordedEtag is the validator the block was built from
	// (arena bytes on the framed path, valid through finish) and
	// recordedStatus the status the block was built for (200 or 304).
	recordedHead   []byte
	precomposed    bool
	recordedEtag   []byte
	recordedStatus int
}

func (w *responseBuffer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// Unwrap exposes the wrapped writer. It exists so walks that end at this
// wrapper (the precomposed-receiver walk) can continue down the chain; the
// writers that hold a *responseBuffer are found by direct type assertion,
// not through Unwrap, so unwrapping changes nothing for them.
func (w *responseBuffer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
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
	if w.assembly != nil {
		assembly := w.assembly
		w.assembly = nil
		defer assembly.release()
	}
	if w.status == 0 {
		w.status = 200
	}
	// The precomposed path emits the recorded head and body directly to the
	// owned writer; every header is already in the head block, so the map
	// work below is skipped wholesale. Ordering matches the map path: the
	// 304 check first, then the head, then the body.
	// The precomposed path's freshness was decided in writeRecordedPieces
	// (same validator, same request); the 304 was deferred to this emission,
	// and a 200 frame carries its body. The receiver prepends the status
	// line, Date and the close decision.
	if w.precomposed && len(w.recordedHead) > 0 {
		if target := findPrecomposedReceiver(w.ResponseWriter); target != nil {
			var body [][]byte
			if len(w.encoded) > 0 {
				body = w.partsBuf[:0]
				body = append(body, w.encoded)
			} else {
				body = w.parts
			}
			_ = target.WritePrecomposed(w.recordedStatus, w.recordedHead, body)
			// A write error means the connection is gone; writing anything
			// further (the map path would re-negotiate encoding over bytes
			// that are already encoded) can only corrupt the moot response.
			// The receiver can only be missing if the chain changed between
			// writeRecordedPieces and finish, which nothing does.
			return
		}
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
