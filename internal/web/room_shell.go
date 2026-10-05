package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/splice"
)

// A cached shell is kept cut at its markers: text, or a slot a request fills.
type shellPart struct {
	recordedPart
	slot int
}

const (
	slotText = iota
	slotLoadedAt
	slotMessages
)

func splitShell(html, messageMarker, loadedMarker string, deflate bool) []shellPart {
	var parts []shellPart
	for html != "" {
		at, marker, slot := len(html), "", slotText
		if i := strings.Index(html, messageMarker); i >= 0 {
			at, marker, slot = i, messageMarker, slotMessages
		}
		if i := strings.Index(html[:at], loadedMarker); i >= 0 {
			at, marker, slot = i, loadedMarker, slotLoadedAt
		}
		if at > 0 {
			piece := splice.Piece{Plain: []byte(html[:at])}
			if deflate {
				piece = splice.Deflate(piece.Plain)
			}
			parts = append(parts, shellPart{recordedPart: recordedPart{piece, sha256.Sum256(piece.Plain), true}})
		}
		if slot != slotText {
			parts = append(parts, shellPart{slot: slot})
		}
		html = html[at+len(marker):]
	}
	return parts
}

// Cache only the surrounding HTML. Authorization and page data are read afresh;
// messages and the refresh timestamp are inserted separately for each request.
func (s *Server) roomShell(p page, messages fragmentEntry) ([]recordedPart, error) {
	loadedAt := p.LoadedAt
	p.Messages, p.MessagesHTML, p.LoadedAt = nil, "", ""
	// Key the entire remaining page so user, room, account, flash, origin, platform
	// and future template inputs cannot accidentally share an incompatible shell.
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("room-shell/%x", sha256.Sum256(raw))
	entry, ok := s.fragments.entry(key)
	if !ok {
		messageMarker := "\x00campfire-" + rand.Text() + "\x00"
		loadedMarker := "campfire-loaded-" + rand.Text()
		p.MessagesHTML, p.LoadedAt = template.HTML(messageMarker), loadedMarker
		b := borrowBuffer()
		defer releaseBuffer(b)
		if err := s.templates.ExecuteTemplate(b, "room", p); err != nil {
			return nil, err
		}
		entry = s.fragments.putEntry(fragmentEntry{key: key, html: template.HTML(b.String()), messageMarker: messageMarker, loadedMarker: loadedMarker})
	}
	shell := entry.shell
	if shell == nil {
		// Too large for the cache, so not deflated ahead either.
		shell = splitShell(string(entry.html), entry.messageMarker, entry.loadedMarker, false)
	}
	// The timestamp goes out stored, so no compressor runs between the cached parts.
	stamp := recordedPart{Piece: splice.Stored([]byte(loadedAt))}
	parts, placed := make([]recordedPart, len(shell)), false
	for i, part := range shell {
		switch part.slot {
		case slotLoadedAt:
			parts[i] = stamp
		case slotMessages:
			parts[i], placed = messages.part(), true
		default:
			parts[i] = part.recordedPart
		}
	}
	if !placed {
		return nil, errors.New("missing message insertion point")
	}
	return parts, nil
}
