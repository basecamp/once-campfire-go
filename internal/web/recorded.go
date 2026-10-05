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
	"github.com/basecamp/once-campfire-go/internal/splice"
)

// A part of a recorded page: its text, deflated ahead when it comes from the cache, and
// the text's digest where one is already known.
type recordedPart struct {
	splice.Piece
	digest   [32]byte
	digested bool
}

func textPart(text string) recordedPart {
	return recordedPart{Piece: splice.Piece{Plain: []byte(text)}}
}

// text is page text rendered for this response. Text that repeats between responses (the
// search page around its results) keeps its deflate by digest, as Rust's GZIPPED does.
func (c *fragmentCache) text(text string) recordedPart {
	if len(text) < 1024 {
		return textPart(text)
	}
	digest := sha256.Sum256([]byte(text))
	key := fmt.Sprintf("text/%x", digest)
	entry, ok := c.entry(key)
	if !ok {
		entry = c.putEntry(fragmentEntry{key: key, html: template.HTML(text)})
	}
	part := entry.part()
	part.digest = digest
	return part
}
func (e fragmentEntry) part() recordedPart {
	piece := e.piece
	if piece.Plain == nil {
		piece.Plain = []byte(e.html)
	}
	return recordedPart{piece, e.digest, true}
}

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

func (s *Server) writeRecorded(w http.ResponseWriter, status int, rendered, marker string, fragment fragmentEntry) {
	before, after, found := strings.Cut(rendered, marker)
	if !found {
		http.Error(w, "Missing message insertion point", 500)
		return
	}
	writeParts(w, status, []recordedPart{s.fragments.text(before), fragment.part(), s.fragments.text(after)})
}

// writeFragment sends a cached fragment as the whole body, with the validator
// responseBuffer.finish gives the same bytes when a 200 renders them afresh.
func writeFragment(w http.ResponseWriter, status int, fragment fragmentEntry) {
	if w.Header().Get("ETag") == "" {
		w.Header().Set("ETag", fmt.Sprintf("W/\"%x\"", fragment.digest[:16]))
	}
	writeParts(w, status, []recordedPart{fragment.part()})
}
func writeParts(w http.ResponseWriter, status int, parts []recordedPart) {
	if w.Header().Get("ETag") == "" {
		// Like Rust's Body::Parts, digest boundaries and cached fragment hashes.
		hash := sha256.New()
		for _, part := range parts {
			var size [8]byte
			binary.LittleEndian.PutUint64(size[:], uint64(len(part.Plain)))
			hash.Write(size[:])
			digest := part.digest
			if !part.digested {
				digest = sha256.Sum256(part.Plain)
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
	pieces := make([]splice.Piece, len(parts))
	for i, part := range parts {
		pieces[i] = part.Piece
	}
	target := w
	for {
		if buffered, ok := target.(*responseBuffer); ok {
			buffered.parts = pieces
			return
		}
		if wrapper, ok := target.(interface{ Unwrap() http.ResponseWriter }); ok {
			target = wrapper.Unwrap()
		} else {
			break
		}
	}
	for _, piece := range pieces {
		w.Write(piece.Plain)
	}
}
