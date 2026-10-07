package web

import (
	"crypto/sha256"
	"fmt"
	"html/template"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Cache only the frame, not the application/Turbo-Frame layout. The renderer
// reads the account, flash, request mode and user profile afresh for that layout.
func (s *Server) sidebarHTML(p page) (template.HTML, error) {
	key := sidebarCacheKey(p)
	if fragment, ok := s.fragments.get(key); ok {
		return fragment, nil
	}
	html, err := s.markup("sidebar-frame", p)
	if err != nil {
		return "", err
	}
	fragment := template.HTML(html)
	s.fragments.put(key, fragment)
	return fragment, nil
}

// Key every value the sidebar frame reads. Authorization and membership data
// are still read afresh before looking up the rendered fragment.
func sidebarCacheKey(p page) string {
	var key strings.Builder
	user := func(u database.User) {
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
