package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// A cached page shell is split once around its per-request insertions; static
// segments keep their bytes and digests, so a hit copies and hashes neither.
type shellSegment struct {
	part recordedPart
	slot shellSlot
}
type shellSlot uint8

const (
	shellStatic shellSlot = iota
	shellMessages
	shellLoadedAt
)

// Cache only the HTML surrounding a page's message list (rooms and searches), or a
// whole page without one (the sidebar). Authorization and page data are read afresh;
// messages and the refresh timestamp are inserted separately for each request.
func (s *Server) pageShell(name string, p page) ([]shellSegment, error) {
	count := p.MessageCount()
	p.Messages, p.records, p.MessagesHTML, p.LoadedAt = nil, nil, "", ""
	// Key the entire remaining page so user, room, account, flash, origin, platform
	// and future template inputs cannot accidentally share an incompatible shell.
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s-shell/%d/%x", name, count, sha256.Sum256(raw))
	if entry, ok := s.fragments.entry(key); ok {
		return entry.segments, nil
	}
	messageMarker := "\x00campfire-" + rand.Text() + "\x00"
	loadedMarker := "campfire-loaded-" + rand.Text()
	p.MessagesHTML, p.LoadedAt, p.records = template.HTML(messageMarker), loadedMarker, make([]database.Message, count)
	b := borrowBuffer()
	defer releaseBuffer(b)
	if err := s.templates.ExecuteTemplate(b, name, p); err != nil {
		return nil, err
	}
	segments, err := splitShell(b.String(), messageMarker, loadedMarker, name != "sidebar")
	if err != nil {
		return nil, err
	}
	return s.fragments.putEntry(fragmentEntry{key: key, segments: segments}).segments, nil
}

func splitShell(html, messageMarker, loadedMarker string, messageList bool) ([]shellSegment, error) {
	var segments []shellSegment
	static := func(text string) {
		if text != "" {
			segments = append(segments, shellSegment{part: newRecordedPart([]byte(text))})
		}
	}
	messages := 0
	for {
		m, l := strings.Index(html, messageMarker), strings.Index(html, loadedMarker)
		if m < 0 && l < 0 {
			static(html)
			break
		}
		if l < 0 || m >= 0 && m < l {
			static(html[:m])
			segments = append(segments, shellSegment{slot: shellMessages})
			html = html[m+len(messageMarker):]
			messages++
		} else {
			static(html[:l])
			segments = append(segments, shellSegment{slot: shellLoadedAt})
			html = html[l+len(loadedMarker):]
		}
	}
	if messages != 1 && messageList || messages != 0 && !messageList || !messageList && len(segments) > 1 {
		return nil, errors.New("missing message insertion point")
	}
	return segments, nil
}

// The response parts for a shell with this request's messages and timestamp.
func shellParts(segments []shellSegment, messages recordedPart, loadedAt string) []recordedPart {
	parts := make([]recordedPart, len(segments))
	var loaded *recordedPart
	for i, segment := range segments {
		switch segment.slot {
		case shellMessages:
			parts[i] = messages
		case shellLoadedAt:
			if loaded == nil {
				part := newRecordedPart([]byte(loadedAt))
				loaded = &part
			}
			parts[i] = *loaded
		default:
			parts[i] = segment.part
		}
	}
	return parts
}
