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
	var key strings.Builder
	key.WriteString("message-list/")
	for _, message := range messages {
		key.WriteString(messageCacheKey(message))
		key.WriteByte('/')
	}
	if entry, ok := s.fragments.entry(key.String()); ok {
		return entry, nil
	}
	views, err := s.messageItems(ctx, messages)
	if err != nil {
		return fragmentEntry{}, err
	}
	var body strings.Builder
	for _, view := range views {
		body.WriteString(string(view.Fragment))
	}
	html := template.HTML(body.String())
	s.fragments.put(key.String(), html)
	if entry, ok := s.fragments.entry(key.String()); ok {
		return entry, nil
	}
	return fragmentEntry{html: html, digest: sha256.Sum256([]byte(html))}, nil
}

func writeRecorded(w http.ResponseWriter, status int, rendered, marker string, fragment fragmentEntry) {
	before, after, found := strings.Cut(rendered, marker)
	if !found {
		http.Error(w, "Missing message insertion point", 500)
		return
	}
	payload := fragment.payload
	if payload == nil {
		payload = []byte(fragment.html)
	}
	parts := [][]byte{[]byte(before), payload, []byte(after)}
	if w.Header().Get("ETag") == "" {
		// Like Rust's Body::Parts, digest boundaries and cached fragment hashes.
		hash := sha256.New()
		for i, part := range parts {
			var size [8]byte
			binary.LittleEndian.PutUint64(size[:], uint64(len(part)))
			hash.Write(size[:])
			digest := fragment.digest
			if i != 1 {
				digest = sha256.Sum256(part)
			}
			hash.Write(digest[:])
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
	target := w
	for {
		if buffered, ok := target.(*responseBuffer); ok {
			buffered.parts = parts
			return
		}
		if wrapper, ok := target.(interface{ Unwrap() http.ResponseWriter }); ok {
			target = wrapper.Unwrap()
		} else {
			break
		}
	}
	for _, part := range parts {
		w.Write(part)
	}
}
