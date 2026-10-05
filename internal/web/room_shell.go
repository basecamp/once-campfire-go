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
	return s.pageShell("room", p)
}

func (s *Server) pageShell(name string, p page) (string, string, error) {
	entry, err := s.pageShellEntry(name, p)
	if err != nil {
		return "", "", err
	}
	return strings.ReplaceAll(string(entry.html), entry.loadedMarker, p.LoadedAt), entry.messageMarker, nil
}

func (s *Server) pageShellEntry(name string, p page) (fragmentEntry, error) {
	messageCount := 0
	if name == "search" {
		messageCount = len(p.Messages)
		if p.messageRecords != nil {
			messageCount = len(p.messageRecords)
		}
	}
	p.messageRecords = nil
	p.Messages, p.MessagesHTML, p.LoadedAt = nil, "", ""
	// Key the entire remaining page so user, room, account, flash, origin, platform
	// and future template inputs cannot accidentally share an incompatible shell.
	raw, err := json.Marshal(struct {
		Page         page
		MessageCount int
	}{p, messageCount})
	if err != nil {
		return fragmentEntry{}, err
	}
	key := fmt.Sprintf("page-shell/%s/%x", name, sha256.Sum256(raw))
	entry, ok := s.fragments.entry(key)
	if !ok {
		messageMarker := "\x00campfire-" + rand.Text() + "\x00"
		loadedMarker := "campfire-loaded-" + rand.Text()
		p.MessagesHTML, p.LoadedAt = template.HTML(messageMarker), loadedMarker
		if name == "search" {
			p.Messages = make([]messageView, messageCount)
		}
		b := borrowBuffer()
		defer releaseBuffer(b)
		if err := s.templates.ExecuteTemplate(b, name, p); err != nil {
			return fragmentEntry{}, err
		}
		entry = fragmentEntry{key: key, html: template.HTML(b.String()), messageMarker: messageMarker, loadedMarker: loadedMarker}
		entry.shell = splitShell(b.String(), messageMarker, loadedMarker)
		entry = s.fragments.putEntry(entry)
	}
	return entry, nil
}

// Fixed chunks retain both their bytes and digest. A request inserts only its
// timestamp and message fragment instead of copying and hashing the whole shell.
type shellPart struct {
	payload []byte
	digest  [32]byte
	slot    byte
}

func splitShell(raw, messages, loaded string) []shellPart {
	var parts []shellPart
	for raw != "" {
		m, l := strings.Index(raw, messages), strings.Index(raw, loaded)
		index, slot, marker := m, byte(1), messages
		if l >= 0 && (m < 0 || l < m) {
			index, slot, marker = l, 2, loaded
		}
		if index < 0 {
			payload := []byte(raw)
			parts = append(parts, shellPart{payload: payload, digest: sha256.Sum256(payload)})
			break
		}
		if index > 0 {
			payload := []byte(raw[:index])
			parts = append(parts, shellPart{payload: payload, digest: sha256.Sum256(payload)})
		}
		parts = append(parts, shellPart{slot: slot})
		raw = raw[index+len(marker):]
	}
	return parts
}
