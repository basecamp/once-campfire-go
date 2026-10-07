package web

import (
	"bytes"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestSidebarCacheTracksRenderedChanges(t *testing.T) {
	app, _, _, user := testApp(t)
	makePage := func() page {
		return page{
			User: user, CanCreateRooms: true, RoomsStream: "rooms", UserRoomsStream: "user",
			SidebarRooms: []sidebarRoom{
				{Room: database.Room{ID: 1, Name: "Chat", Type: "Rooms::Open"}},
				{
					Room:    database.Room{ID: 2, Type: "Rooms::Direct"},
					Members: []database.User{{ID: 2, Name: "Second Person"}},
				},
			},
			Placeholders: []database.User{{ID: 3, Name: "Third Person"}},
		}
	}
	render := func(p page) string {
		var b bytes.Buffer
		if err := app.templates.ExecuteTemplate(&b, "sidebar-frame", p); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	original := makePage()
	body, key := render(original), sidebarCacheKey(original)
	changes := map[string]func(*page){
		"unread":             func(p *page) { p.SidebarRooms[0].Unread = true },
		"rename":             func(p *page) { p.SidebarRooms[0].Name = "Renamed" },
		"membership removed": func(p *page) { p.SidebarRooms = p.SidebarRooms[1:] },
		"room permission":    func(p *page) { p.CanCreateRooms = false },
		"member name":        func(p *page) { p.SidebarRooms[1].Members[0].Name = "Changed Person" },
		"member avatar":      func(p *page) { p.SidebarRooms[1].Members[0].UpdatedAt = time.Now() },
		"own avatar":         func(p *page) { p.User.UpdatedAt = p.User.UpdatedAt.Add(time.Second) },
		"placeholder":        func(p *page) { p.Placeholders[0].Name = "Different Person" },
		"stream":             func(p *page) { p.UserRoomsStream = "different" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := makePage()
			change(&p)
			if render(p) == body {
				t.Fatal("test must change rendered HTML")
			}
			if sidebarCacheKey(p) == key {
				t.Fatal("changed HTML reused cache key")
			}
		})
	}
}
