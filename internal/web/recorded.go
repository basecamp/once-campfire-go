package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

// The marker exists only during template execution. The actual response inserts
// the cached message list without copying it through template/fmt/page buffers.

func (s *Server) messageList(ctx context.Context, messages []database.Message) (responsebody.Part, error) {
	key := messageListCacheKey(messages)
	if entry, ok := s.fragments.entry(key); ok {
		return entry.part, nil
	}
	views, err := s.messageItems(ctx, messages)
	if err != nil {
		return responsebody.Part{}, err
	}
	size := 0
	for _, view := range views {
		size += len(view.Fragment)
	}
	body := make([]byte, size)
	offset := 0
	for _, view := range views {
		offset += copy(body[offset:], view.Fragment)
	}
	// This buffer is not pooled: the Part takes ownership through eviction and
	// any outstanding responses, without retaining a second HTML string.
	entry := s.fragments.putEntry(fragmentEntry{key: key, part: responsebody.NewPart(body)})
	return entry.part, nil
}

// Retain the actual Part on a hit: eviction between lookup and rendering cannot
// turn a references-only search into an unscoped, newer-body hydration.
func (s *Server) searchMessageList(ctx context.Context, user int64, query string, refs []database.Message) (responsebody.Part, int, error) {
	if len(refs) == 0 {
		return responsebody.Part{}, 0, nil
	}
	if entry, ok := s.fragments.entry(messageListCacheKey(refs)); ok {
		return entry.part, len(refs), nil
	}
	// Matching membership and body must come from one statement on a miss.
	// An edit/delete after the reference query can change the result set.
	messages, err := s.DB.Search(ctx, user, query)
	if err != nil {
		return responsebody.Part{}, 0, err
	}
	part, err := s.messageList(ctx, messages)
	return part, len(messages), err
}

func writeRecorded(w http.ResponseWriter, status int, rendered, marker string, part responsebody.Part) {
	before, after, found := strings.Cut(rendered, marker)
	if !found {
		http.Error(w, "Missing message insertion point", 500)
		return
	}
	parts := []responsebody.Part{responsebody.NewPart([]byte(before)), part, responsebody.NewPart([]byte(after))}
	writeParts(w, status, parts)
}

func writeParts(w http.ResponseWriter, status int, parts []responsebody.Part) {
	if w.Header().Get("ETag") == "" {
		digest := responsebody.Digest(parts)
		w.Header().Set("ETag", fmt.Sprintf("W/\"%x\"", digest[:16]))
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
		part.WriteTo(w)
	}
}
