package presentation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

type sidebarShellPage struct {
	layoutShellPage
	SidebarHTML template.HTML
}

// Memberships and layout observations are read afresh before selecting parts.
// Frame and surrounding bytes have independent identities: a profile, flash or
// account change replaces the layout without rendering an unchanged frame again.
func (f *Fragments) SidebarParts(layout LayoutInput, p SidebarInput) ([]responsebody.Part, error) {
	frameKey := sidebarCacheKey(p)
	frame, ok := f.cache.entry(frameKey)
	if !ok {
		html, err := f.renderer.Markup("sidebar-frame", p)
		if err != nil {
			return nil, err
		}
		frame = f.cache.putEntry(fragmentEntry{
			key: frameKey, part: responsebody.NewPart([]byte(html)),
		})
	}

	input := sidebarShellPage{layoutShellPage: shellPage(layout)}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("sidebar-shell/%x", sha256.Sum256(raw))
	entry, ok := f.cache.entry(key)
	if !ok {
		marker := "\x00campfire-" + rand.Text() + "\x00"
		input.SidebarHTML = template.HTML(marker)
		b := new(bytes.Buffer)
		if err := f.renderer.ExecuteTemplate(b, "sidebar", input); err != nil {
			return nil, err
		}
		rendered := b.String()
		before, after, found := strings.Cut(rendered, marker)
		if !found || strings.Count(rendered, marker) != 1 {
			return nil, fmt.Errorf("sidebar template must contain one frame insertion point")
		}
		entry = f.cache.putEntry(fragmentEntry{key: key, shell: &templateShell{
			parts: []responsebody.Part{
				responsebody.NewPart([]byte(before)), responsebody.NewPart([]byte(after)),
			},
			bytes: len(before) + len(after),
		}})
	}
	return []responsebody.Part{entry.shell.parts[0], frame.part, entry.shell.parts[1]}, nil
}

// Key every value the sidebar frame reads. Authorization and membership data
// are still read afresh before looking up the rendered fragment.
func sidebarCacheKey(p SidebarInput) string {
	var key strings.Builder
	user := func(u database.RoomParticipant) {
		fmt.Fprintf(&key, "u%d/%d/%d:%s/", u.ID, u.UpdatedAt.UnixMicro(), len(u.Name), u.Name)
	}
	user(p.User)
	fmt.Fprintf(&key, "%t/%s/%s/", p.CanCreateRooms, p.RoomsStream, p.UserRoomsStream)
	for _, room := range p.SidebarRooms {
		fmt.Fprintf(
			&key,
			"r%d/%d/%t/%d:%s/%d:%s/",
			room.ID,
			room.UpdatedAt.UnixMicro(),
			room.Unread,
			len(room.Type),
			room.Type,
			len(room.Name),
			room.Name,
		)
		for _, member := range room.Members {
			user(member)
		}
		key.WriteByte(';')
	}
	key.WriteByte('|')
	for _, member := range p.Placeholders {
		user(member)
	}
	return fmt.Sprintf("sidebar/%x", sha256.Sum256([]byte(key.String())))
}

type SidebarInput struct {
	User                         database.RoomParticipant
	SidebarRooms                 []RoomView
	Placeholders                 []database.RoomParticipant
	RoomsStream, UserRoomsStream string
	CanCreateRooms               bool
}
