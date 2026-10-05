package web

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"html/template"
	"strings"
)

const (
	slotStatic byte = iota
	slotTime
	slotMessages
)

// shellSlot is one slice of a cached room page. Static slices keep their
// digest and compressed copies; the timestamp and the message list are joined
// on each request.
type shellSlot struct {
	kind        byte
	data        []byte
	gzip, zstd  []byte
	digest      [32]byte
	digestReady bool
}

// Cache only the surrounding HTML. Authorization and page data are read afresh;
// messages and the refresh timestamp are inserted separately for each request.
func (s *Server) roomShell(p page) (string, string, error) {
	entry, err := s.roomShellEntry(p)
	if err != nil {
		return "", "", err
	}
	var b []byte
	for _, slot := range entry.slots {
		switch slot.kind {
		case slotTime:
			b = append(b, p.LoadedAt...)
		case slotMessages:
			b = append(b, entry.messageMarker...)
		default:
			b = append(b, slot.data...)
		}
	}
	return string(b), entry.messageMarker, nil
}

func (s *Server) roomShellEntry(p page) (fragmentEntry, error) {
	p.Messages, p.MessagesHTML, p.LoadedAt = nil, "", ""
	key := roomShellKey(p)
	if entry, ok := s.fragments.entry(key); ok && len(entry.slots) > 0 {
		s.stats.shellHit.Add(1)
		return entry, nil
	}
	s.stats.shellMiss.Add(1)
	messageMarker := "\x00campfire-" + rand.Text() + "\x00"
	loadedMarker := "campfire-loaded-" + rand.Text()
	p.MessagesHTML, p.LoadedAt = template.HTML(messageMarker), loadedMarker
	buf := borrowBuffer()
	defer releaseBuffer(buf)
	if err := s.templates.ExecuteTemplate(buf, "room", p); err != nil {
		return fragmentEntry{}, err
	}
	entry := fragmentEntry{key: key, messageMarker: messageMarker, loadedMarker: loadedMarker, slots: splitShell(buf.String(), loadedMarker, messageMarker)}
	return s.fragments.putEntry(entry), nil
}

func roomShellKey(p page) string {
	plat := p.Platform
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"frame=%t\ttitle=%s\tuid=%d\tname=%s\tbio=%s\trole=%d\tuat=%d\tvapid=%s\tacc=%d\tlogo=%t\tjoin=%s\tstyles=%s\treload=%t\tchat=%t\trid=%d\trtype=%s\trname=%s\tbody=%s\tscreen=%s\tnotice=%s\terr=%s\torigin=%s\tinv=%t\tstream=%s\tver=%s\tplat=%t/%t/%t/%t/%t/%t/%t/%t/%t/%t/%t/%s/%s",
		p.Frame, p.Title, p.User.ID, p.User.Name, p.User.Bio, p.User.Role, p.User.UpdatedAt.UnixMicro(), p.VAPIDPublicKey,
		p.Account.UpdatedAt.UnixMicro(), p.Account.HasLogo, p.Account.JoinCode, string(p.CustomStyles), p.Reload, p.Chat,
		p.Room.ID, p.Room.Type, p.Room.Name, p.BodyClass, p.Screen, p.Notice, p.Error, p.Origin, p.Invitation, p.Stream, p.Version,
		plat.IOS, plat.Android, plat.Mac, plat.Windows, plat.Chrome, plat.Firefox, plat.Safari, plat.Edge, plat.Mobile, plat.Desktop, plat.AppleMessages, plat.Browser, plat.OperatingSystem,
	)))
	return fmt.Sprintf("room-shell/%x", sum[:])
}

func splitShell(html, loaded, message string) []shellSlot {
	li, mi := strings.Index(html, loaded), strings.Index(html, message)
	if li < 0 || mi < 0 {
		return []shellSlot{staticSlot(html)}
	}
	type mark struct {
		at, n int
		kind  byte
	}
	marks := []mark{{li, len(loaded), slotTime}, {mi, len(message), slotMessages}}
	if marks[0].at > marks[1].at {
		marks[0], marks[1] = marks[1], marks[0]
	}
	slots := make([]shellSlot, 0, 5)
	cursor := 0
	for _, mark := range marks {
		if mark.at > cursor {
			slots = append(slots, staticSlot(html[cursor:mark.at]))
		}
		slots = append(slots, shellSlot{kind: mark.kind})
		cursor = mark.at + mark.n
	}
	if cursor < len(html) {
		slots = append(slots, staticSlot(html[cursor:]))
	}
	return slots
}

func staticSlot(s string) shellSlot {
	data := []byte(s)
	slot := shellSlot{kind: slotStatic, data: data, digest: sha256.Sum256(data), digestReady: true}
	if len(data) >= 1024 {
		slot.gzip = gzipMember(data)
		slot.zstd = zstdMember(data)
	}
	return slot
}
