package web

import (
	"bytes"
	"github.com/basecamp/once-campfire-go/internal/database"
	"html/template"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/useragent"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func TestRoomShellPreservesBytesAndRequestData(t *testing.T) {
	app, _, _, user := testApp(t)
	base := page{User: user, Room: database.Room{ID: 1, Name: "Room & <name>", Type: "Rooms::Open"}, Chat: true, Screen: "room", Origin: "https://example.test", LoadedAt: "1234567890", MessagesHTML: template.HTML("<div>message one</div>")}
	check := func(p page) {
		t.Helper()
		var expected bytes.Buffer
		if err := app.templates.ExecuteTemplate(&expected, "room", p); err != nil {
			t.Fatal(err)
		}
		parts, err := app.roomParts(p, responsebody.NewPart([]byte(p.MessagesHTML)))
		if err != nil {
			t.Fatal(err)
		}
		var actual bytes.Buffer
		for _, part := range parts {
			if _, err := part.WriteTo(&actual); err != nil {
				t.Fatal(err)
			}
		}
		if actual.String() != expected.String() {
			t.Fatal("cached room shell differs from uncached template")
		}
	}
	check(base)
	check(base)
	changes := map[string]func(*page){
		"timestamp and messages": func(p *page) { p.LoadedAt = "1234567999"; p.MessagesHTML = "<p>new message</p>" },
		"user":                   func(p *page) { p.User.Name = "Other <person>"; p.User.ID++ },
		"role":                   func(p *page) { p.User.Role = 0 },
		"room":                   func(p *page) { p.Room.Name = "Renamed" },
		"flash":                  func(p *page) { p.Notice = "Saved" },
		"error":                  func(p *page) { p.Error = "Failed" },
		"styles":                 func(p *page) { p.CustomStyles = "<style>body{color:red}</style>" },
		"origin":                 func(p *page) { p.Origin = "https://other.test" },
		"frame":                  func(p *page) { p.Frame = true },
		"invitation":             func(p *page) { p.Invitation = true; p.Account.JoinCode = "new-code" },
		"invite code":            func(p *page) { p.Invitation = true; p.Account.JoinCode = "rotated-code" },
		"user bio and avatar":    func(p *page) { p.User.Bio = "New bio"; p.User.UpdatedAt = p.User.UpdatedAt.Add(time.Second) },
		"direct room":            func(p *page) { p.Room.Type = "Rooms::Direct" },
		"account logo":           func(p *page) { p.Account.HasLogo = true; p.Account.UpdatedAt = time.Unix(1700000000, 0) },
		"vapid":                  func(p *page) { p.VAPIDPublicKey = "new-public-key" },
		"platform": func(p *page) {
			p.Platform = useragent.Platform{IOS: true, Safari: true, Mobile: true, Browser: "Safari", OperatingSystem: "iPhone"}
		},
		"desktop platform": func(p *page) {
			p.Platform = useragent.Platform{Windows: true, Chrome: true, Desktop: true, Browser: "Chrome", OperatingSystem: "Windows"}
		},
		"stream": func(p *page) { p.Stream = "new-stream" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { p := base; change(&p); check(p); check(base) })
	}
	// Oversized entries bypass the bounded cache but must still render correctly.
	app.fragments = newFragmentCache(1)
	check(base)
}
