package presentation

import (
	"bytes"
	"html/template"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestSidebarPartsMatchTemplates(t *testing.T) {
	app, user := shellTestFragments(t)
	original := shellFixture{
		User: user, Screen: "sidebar", CanCreateRooms: true,
		RoomsStream: "rooms", UserRoomsStream: "user", VAPIDPublicKey: "push-key",
		Account: database.Account{ID: 1, UpdatedAt: time.Unix(1700000000, 0)},
		SidebarRooms: []RoomView{
			{Room: database.Room{ID: 1, Name: "Chat", Type: "Rooms::Open"}},
		},
	}
	changes := []struct {
		name   string
		change func(*shellFixture)
	}{
		{"unchanged", func(*shellFixture) {}},
		{
			"profile and escaping",
			func(p *shellFixture) { p.User.Name = `<name "quoted">`; p.User.Role = 0 },
		},
		{
			"account logo",
			func(p *shellFixture) {
				p.Account.HasLogo = true
				p.Account.UpdatedAt = p.Account.UpdatedAt.Add(time.Second)
			},
		},
		{
			"custom styles",
			func(p *shellFixture) { p.CustomStyles = template.HTML("<style>body{color:red}</style>") },
		},
		{"flash", func(p *shellFixture) { p.Notice = "Saved <changes>" }},
		{
			"permission and streams",
			func(p *shellFixture) { p.CanCreateRooms = false; p.UserRoomsStream = "fresh" },
		},
	}
	for _, enabled := range []bool{true, false} {
		if !enabled {
			app.cache = newFragmentCache(0)
		}
		for _, frame := range []bool{false, true} {
			for _, change := range changes {
				t.Run(change.name, func(t *testing.T) {
					p := original
					p.Frame = frame
					change.change(&p)
					fragment, err := app.renderer.Markup("sidebar-frame", p)
					if err != nil {
						t.Fatal(err)
					}
					input := struct {
						shellFixture
						SidebarHTML template.HTML
					}{p, template.HTML(fragment)}
					var want bytes.Buffer
					if err := app.renderer.ExecuteTemplate(&want, "sidebar", input); err != nil {
						t.Fatal(err)
					}
					for range 2 {
						parts, err := app.SidebarParts(p.layout(), p.sidebar())
						if err != nil {
							t.Fatal(err)
						}
						var got bytes.Buffer
						for _, part := range parts {
							if _, err := part.WriteTo(&got); err != nil {
								t.Fatal(err)
							}
						}
						if !bytes.Equal(got.Bytes(), want.Bytes()) {
							t.Fatalf(
								"parts differ from complete template: cache=%t frame=%t",
								enabled,
								frame,
							)
						}
					}
				})
			}
		}
	}
}

func TestSidebarCacheTracksRenderedChanges(t *testing.T) {
	app, user := shellTestFragments(t)
	makePage := func() shellFixture {
		return shellFixture{
			User: user, CanCreateRooms: true, RoomsStream: "rooms", UserRoomsStream: "user",
			SidebarRooms: []RoomView{
				{Room: database.Room{ID: 1, Name: "Chat", Type: "Rooms::Open"}},
				{
					Room:    database.Room{ID: 2, Type: "Rooms::Direct"},
					Members: []database.RoomParticipant{{ID: 2, Name: "Second Person"}},
				},
			},
			Placeholders: []database.RoomParticipant{{ID: 3, Name: "Third Person"}},
		}
	}
	render := func(p shellFixture) string {
		var b bytes.Buffer
		if err := app.renderer.ExecuteTemplate(&b, "sidebar-frame", p); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	original := makePage()
	body, key := render(original), sidebarCacheKey(original.sidebar())
	changes := map[string]func(*shellFixture){
		"unread":             func(p *shellFixture) { p.SidebarRooms[0].Unread = true },
		"rename":             func(p *shellFixture) { p.SidebarRooms[0].Name = "Renamed" },
		"membership removed": func(p *shellFixture) { p.SidebarRooms = p.SidebarRooms[1:] },
		"room permission":    func(p *shellFixture) { p.CanCreateRooms = false },
		"member name":        func(p *shellFixture) { p.SidebarRooms[1].Members[0].Name = "Changed Person" },
		"member avatar":      func(p *shellFixture) { p.SidebarRooms[1].Members[0].UpdatedAt = time.Now() },
		"own avatar":         func(p *shellFixture) { p.User.UpdatedAt = p.User.UpdatedAt.Add(time.Second) },
		"placeholder":        func(p *shellFixture) { p.Placeholders[0].Name = "Different Person" },
		"stream":             func(p *shellFixture) { p.UserRoomsStream = "different" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := makePage()
			change(&p)
			if render(p) == body {
				t.Fatal("test must change rendered HTML")
			}
			if sidebarCacheKey(p.sidebar()) == key {
				t.Fatal("changed HTML reused cache key")
			}
		})
	}
}
