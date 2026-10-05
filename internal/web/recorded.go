package web

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"html/template"

	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// The marker exists only during template execution. The actual response inserts
// the cached message list without copying it through template/fmt/page buffers.

func (s *Server) messageList(ctx context.Context, messages []database.Message) (fragmentEntry, error) {
	raw := make([]byte, 0, 16+len(messages)*56)
	raw = append(raw, "message-list/"...)
	for _, message := range messages {
		raw = appendMessageCacheKey(raw, message)
		raw = append(raw, '/')
	}
	if entry, ok := s.fragments.entry(string(raw)); ok {
		return entry, nil
	}
	key := string(raw)
	views, err := s.messageItems(ctx, messages)
	if err != nil {
		return fragmentEntry{}, err
	}
	var body strings.Builder
	for _, view := range views {
		body.WriteString(string(view.Fragment))
	}
	html := template.HTML(body.String())
	s.fragments.put(key, html)
	if entry, ok := s.fragments.entry(key); ok {
		return entry, nil
	}
	return fragmentEntry{html: html, digest: sha256.Sum256([]byte(html))}, nil
}

// A response body part with its SHA-256, cached for fragments and page shells.
type recordedPart struct {
	data   []byte
	digest [32]byte
}

func newRecordedPart(data []byte) recordedPart {
	return recordedPart{data: data, digest: sha256.Sum256(data)}
}
func (f fragmentEntry) part() recordedPart {
	payload := f.payload
	if payload == nil {
		payload = []byte(f.html)
	}
	return recordedPart{data: payload, digest: f.digest}
}

func writeRecorded(w http.ResponseWriter, status int, rendered, marker string, fragment fragmentEntry) {
	before, after, found := strings.Cut(rendered, marker)
	if !found {
		http.Error(w, "Missing message insertion point", 500)
		return
	}
	writeParts(w, status, []recordedPart{newRecordedPart([]byte(before)), fragment.part(), newRecordedPart([]byte(after))})
}

func writeParts(w http.ResponseWriter, status int, parts []recordedPart) {
	if w.Header().Get("ETag") == "" {
		// Like Rust's Body::Parts, digest boundaries and cached part hashes.
		hash := sha256.New()
		for _, part := range parts {
			var size [8]byte
			binary.LittleEndian.PutUint64(size[:], uint64(len(part.data)))
			hash.Write(size[:])
			hash.Write(part.digest[:])
		}
		w.Header().Set("ETag", fmt.Sprintf("W/\"%x\"", hash.Sum(nil)[:16]))
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	}
	w.WriteHeader(status)
	if sw, ok := w.(*sessionWriter); ok && sw.failed {
		return
	}
	body := make([][]byte, len(parts))
	for i, part := range parts {
		body[i] = part.data
	}
	target := w
	for {
		if buffered, ok := target.(*responseBuffer); ok {
			buffered.parts = body
			return
		}
		if wrapper, ok := target.(interface{ Unwrap() http.ResponseWriter }); ok {
			target = wrapper.Unwrap()
		} else {
			break
		}
	}
	for _, part := range body {
		w.Write(part)
	}
}
