package web

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Key every value the sidebar template reads. Authorization and membership data
// are still read afresh before looking up the rendered fragment.
func sidebarCacheKey(p page) string {
	var key strings.Builder
	user := func(u database.User) {
		fmt.Fprintf(&key, "u%d/%d/%d:%s/", u.ID, u.UpdatedAt.UnixMicro(), len(u.Name), u.Name)
	}
	user(p.User)
	fmt.Fprintf(&key, "%t/%s/%s/", p.CanCreateRooms, p.RoomsStream, p.UserRoomsStream)
	for _, room := range p.SidebarRooms {
		fmt.Fprintf(&key, "r%d/%d/%t/%d:%s/%d:%s/", room.ID, room.UpdatedAt.UnixMicro(), room.Unread, len(room.Type), room.Type, len(room.Name), room.Name)
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
