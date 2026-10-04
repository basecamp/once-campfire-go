package web

import (
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// The sidebar is a page around its frame (a frame layout for Turbo frame requests), as
// in the reference; its cached HTML must follow every page value.
func TestSidebarShellTracksRenderedChanges(t *testing.T) {
	app, _, _, user := testApp(t)
	base := page{User: user, Screen: "sidebar", CanCreateRooms: true, RoomsStream: "rooms", UserRoomsStream: "user",
		SidebarRooms: []sidebarRoom{{Room: database.Room{ID: 1, Name: "Chat", Type: "Rooms::Open"}}, {Room: database.Room{ID: 2, Type: "Rooms::Direct"}, Members: []database.User{{ID: 2, Name: "Second Person"}}}},
		Placeholders: []database.User{{ID: 3, Name: "Third Person"}}}
	clone := func(p *page) {
		p.SidebarRooms = append([]sidebarRoom(nil), p.SidebarRooms...)
		p.SidebarRooms[1].Members = append([]database.User(nil), p.SidebarRooms[1].Members...)
		p.Placeholders = append([]database.User(nil), p.Placeholders...)
	}
	checkShell(t, app, "sidebar", base, map[string]func(*page){
		"unread":             func(p *page) { clone(p); p.SidebarRooms[0].Unread = true },
		"rename":             func(p *page) { clone(p); p.SidebarRooms[0].Name = "Renamed" },
		"membership removed": func(p *page) { p.SidebarRooms = p.SidebarRooms[1:] },
		"room permission":    func(p *page) { p.CanCreateRooms = false },
		"member name":        func(p *page) { clone(p); p.SidebarRooms[1].Members[0].Name = "Changed Person" },
		"member avatar":      func(p *page) { clone(p); p.SidebarRooms[1].Members[0].UpdatedAt = time.Now() },
		"own avatar":         func(p *page) { p.User.UpdatedAt = p.User.UpdatedAt.Add(time.Second) },
		"placeholder":        func(p *page) { clone(p); p.Placeholders[0].Name = "Different Person" },
		"stream":             func(p *page) { p.UserRoomsStream = "different" },
		"frame":              func(p *page) { p.Frame = true },
		"flash":              func(p *page) { p.Notice = "Saved" },
	})
}
