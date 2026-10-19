package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
)

// Cache only the surrounding HTML. Authorization and page data are read afresh;
// messages and the refresh timestamp are inserted separately for each request.
func (s *Server) roomShell(p page) (string, string, error) {
	loadedAt := p.LoadedAt
	p.Messages, p.MessagesHTML, p.LoadedAt = nil, "", ""
	// Key the entire remaining page so user, room, account, flash, origin, platform
	// and future template inputs cannot accidentally share an incompatible shell.
	raw, err := json.Marshal(p)
	if err != nil {
		return "", "", err
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
			return "", "", err
		}
		entry = s.fragments.putEntry(fragmentEntry{key: key, html: template.HTML(b.String()), messageMarker: messageMarker, loadedMarker: loadedMarker})
	}
	return strings.ReplaceAll(string(entry.html), entry.loadedMarker, loadedAt), entry.messageMarker, nil
}
