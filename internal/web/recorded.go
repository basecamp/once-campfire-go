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
	"github.com/basecamp/once-campfire-go/internal/piececache"
)

// The marker exists only during template execution. The actual response inserts
// the cached message list without copying it through template/fmt/page buffers.

// recordedPayload is a rendered message list in whichever representation the
// active path uses: one immutable piece (raw+gzip member+digest) when
// CAMPFIRE_RECORDED_PIECES is on, or the legacy HTML fragment when it is off.
type recordedPayload struct {
	piece    *piececache.Entry
	fragment fragmentEntry
}

func messageListKey(messages []database.Message) string {
	var key strings.Builder
	key.WriteString("message-list/")
	for _, message := range messages {
		key.WriteString(messageCacheKey(message))
		key.WriteByte('/')
	}
	return key.String()
}

// recordedMessageList returns the message-list payload for a recorded page,
// caching it as a compressed piece on the piece path. The key is content
// derived (message ids and updated-at stamps), so writes invalidate by key
// change alone. needMember asks for the gzip member because this request will
// assemble a gzip body; when the cache cannot store anything and no gzip body
// is being assembled, the compression would be discarded immediately, so it is
// skipped.
func (s *Server) recordedMessageList(ctx context.Context, messages []database.Message, needMember bool) (recordedPayload, error) {
	if !s.recordedPieces {
		entry, err := s.messageList(ctx, messages)
		return recordedPayload{fragment: entry}, err
	}
	key := messageListKey(messages)
	if entry := s.pieces.Get(key); entry != nil {
		return recordedPayload{piece: entry}, nil
	}
	views, err := s.messageItems(ctx, messages)
	if err != nil {
		return recordedPayload{}, err
	}
	var body strings.Builder
	for _, view := range views {
		body.WriteString(string(view.Fragment))
	}
	raw := []byte(body.String())
	var member []byte
	if needMember || s.pieces.Enabled() {
		member = compressGzip(raw)
	}
	entry, _ := s.pieces.Put(key, raw, member)
	return recordedPayload{piece: entry}, nil
}

func (s *Server) messageList(ctx context.Context, messages []database.Message) (fragmentEntry, error) {
	key := messageListKey(messages)
	if entry, ok := s.fragments.entry(key); ok {
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
	s.fragments.put(key, html)
	if entry, ok := s.fragments.entry(key); ok {
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
