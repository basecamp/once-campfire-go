package presentation

import (
	"bytes"
	"html/template"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

func TestRoomShellPreservesBytesAndRequestData(t *testing.T) {
	app, user := shellTestFragments(t)
	base := shellFixture{
		User:         user,
		Room:         database.Room{ID: 1, Name: "Room & <name>", Type: "Rooms::Open"},
		Chat:         true,
		Screen:       "room",
		Origin:       "https://example.test",
		LoadedAt:     "1234567890",
		MessagesHTML: template.HTML("<div>message one</div>"),
	}
	check := func(t *testing.T, p shellFixture) {
		t.Helper()
		var expected bytes.Buffer
		if err := app.renderer.ExecuteTemplate(&expected, "room", p); err != nil {
			t.Fatal(err)
		}
		parts, err := app.RoomParts(p.layout(), p.LoadedAt, responsebody.NewPart([]byte(p.MessagesHTML)))
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
	check(t, base)
	check(t, base)
	t.Run("non-rendered user state reuses shell", func(t *testing.T) {
		entries, size := len(app.cache.entries), app.cache.bytes
		p := base
		p.User.Email = "changed@example.test"
		p.User.Password = "changed password digest"
		p.User.BotToken = "changed bot token"
		p.User.Status = 2
		check(t, p)
		if len(app.cache.entries) != entries || app.cache.bytes != size {
			t.Fatal("non-rendered user state retained another copy of unchanged shell HTML")
		}
	})
	t.Run("room activity reuses shell", func(t *testing.T) {
		entries, size := len(app.cache.entries), app.cache.bytes
		p := base
		p.Room.UpdatedAt = time.Unix(1700000000, 0)
		p.LoadedAt = "1234567999"
		p.MessagesHTML = "<p>new message</p>"
		check(t, p)
		if len(app.cache.entries) != entries || app.cache.bytes != size {
			t.Fatal("room activity retained another copy of unchanged shell HTML")
		}
	})
	changes := map[string]func(*shellFixture){
		"timestamp and messages": func(p *shellFixture) { p.LoadedAt = "1234567999"; p.MessagesHTML = "<p>new message</p>" },
		"user":                   func(p *shellFixture) { p.User.Name = "Other <person>"; p.User.ID++ },
		"role":                   func(p *shellFixture) { p.User.Role = 0 },
		"room":                   func(p *shellFixture) { p.Room.Name = "Renamed" },
		"flash":                  func(p *shellFixture) { p.Notice = "Saved" },
		"error":                  func(p *shellFixture) { p.Error = "Failed" },
		"styles":                 func(p *shellFixture) { p.CustomStyles = "<style>body{color:red}</style>" },
		"origin":                 func(p *shellFixture) { p.Origin = "https://other.test" },
		"frame":                  func(p *shellFixture) { p.Frame = true },
		"invitation":             func(p *shellFixture) { p.Invitation = true; p.Account.JoinCode = "new-code" },
		"invite code":            func(p *shellFixture) { p.Invitation = true; p.Account.JoinCode = "rotated-code" },
		"user bio and avatar":    func(p *shellFixture) { p.User.Bio = "New bio"; p.User.UpdatedAt = p.User.UpdatedAt.Add(time.Second) },
		"direct room":            func(p *shellFixture) { p.Room.Type = "Rooms::Direct" },
		"account logo":           func(p *shellFixture) { p.Account.HasLogo = true; p.Account.UpdatedAt = time.Unix(1700000000, 0) },
		"vapid":                  func(p *shellFixture) { p.VAPIDPublicKey = "new-public-key" },
		"platform": func(p *shellFixture) {
			p.Platform = useragent.Platform{
				IOS:             true,
				Safari:          true,
				Mobile:          true,
				Browser:         "Safari",
				OperatingSystem: "iPhone",
			}
		},
		"desktop platform": func(p *shellFixture) {
			p.Platform = useragent.Platform{
				Windows:         true,
				Chrome:          true,
				Desktop:         true,
				Browser:         "Chrome",
				OperatingSystem: "Windows",
			}
		},
		"stream": func(p *shellFixture) { p.Stream = "new-stream" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { p := base; change(&p); check(t, p); check(t, base) })
	}
	// Oversized entries bypass the bounded cache but must still render correctly.
	app.cache = newFragmentCache(1)
	check(t, base)
}
