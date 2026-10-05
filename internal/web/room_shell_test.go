package web

import (
	"bytes"
	"crypto/sha256"
	"github.com/basecamp/once-campfire-go/internal/database"
	"html/template"
	"strings"
	"testing"
)

func TestRoomShellPreservesBytesAndRequestData(t *testing.T) {
	app, _, _, user := testApp(t)
	base := page{User: user, Room: database.Room{ID: 1, Name: "Room & <name>", Type: "Rooms::Open"}, Chat: true, Screen: "room", Origin: "https://example.test", LoadedAt: "1234567890", MessagesHTML: template.HTML("<div>message one</div>")}
	checkShell(t, app, "room", base, map[string]func(*page){
		"timestamp and messages": func(p *page) { p.LoadedAt = "1234567999"; p.MessagesHTML = "<p>new message</p>" },
		"user":                   func(p *page) { p.User.Name = "Other <person>"; p.User.ID++ },
		"role":                   func(p *page) { p.User.Role = 0 },
		"room":                   func(p *page) { p.Room.Name = "Renamed" },
		"flash":                  func(p *page) { p.Notice = "Saved" },
		"error":                  func(p *page) { p.Error = "Failed" },
		"styles":                 func(p *page) { p.CustomStyles = "<style>body{color:red}</style>" },
		"origin":                 func(p *page) { p.Origin = "https://other.test" },
		"frame":                  func(p *page) { p.Frame = true },
		"invitation":             func(p *page) { p.Invitation = true; p.JoinCode = "new-code" },
		"stream":                 func(p *page) { p.Stream = "new-stream" },
	})
}

func TestSearchShellPreservesBytesAndRequestData(t *testing.T) {
	app, _, _, user := testApp(t)
	base := page{Title: "Search", Query: "coffee & <tea>", User: user, RecentSearches: []string{"coffee", "a \"quote\""}, ReturnRoom: 1, Screen: "search", BodyClass: "searches", Origin: "https://example.test", MessagesHTML: template.HTML("<div>message one</div>"), records: make([]database.Message, 3)}
	checkShell(t, app, "search", base, map[string]func(*page){
		"messages":        func(p *page) { p.MessagesHTML = "<p>new message</p>" },
		"message count":   func(p *page) { p.records = make([]database.Message, 7) },
		"views":           func(p *page) { p.records = nil; p.Messages = make([]messageView, 2) },
		"query":           func(p *page) { p.Query = "other" },
		"recent searches": func(p *page) { p.RecentSearches = append(p.RecentSearches, "new") },
		"no recents":      func(p *page) { p.RecentSearches = nil },
		"return room":     func(p *page) { p.ReturnRoom = 2 },
		"user":            func(p *page) { p.User.Name = "Other <person>"; p.User.ID++ },
		"flash":           func(p *page) { p.Notice = "Saved" },
		"styles":          func(p *page) { p.CustomStyles = "<style>body{color:red}</style>" },
		"frame":           func(p *page) { p.Frame = true },
	})
}

func checkShell(t *testing.T, app *Server, name string, base page, changes map[string]func(*page)) {
	t.Helper()
	check := func(p page) {
		t.Helper()
		var expected bytes.Buffer
		if err := app.templates.ExecuteTemplate(&expected, name, p); err != nil {
			t.Fatal(err)
		}
		segments, err := app.pageShell(name, p)
		if err != nil {
			t.Fatal(err)
		}
		var actual strings.Builder
		for _, part := range shellParts(segments, newRecordedPart([]byte(p.MessagesHTML)), p.LoadedAt) {
			if part.digest != sha256.Sum256(part.data) {
				t.Fatal("stale part digest")
			}
			actual.Write(part.data)
		}
		if actual.String() != expected.String() {
			t.Fatalf("cached %s shell differs from uncached template", name)
		}
	}
	check(base)
	check(base)
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { p := base; change(&p); check(p); check(base) })
	}
	// Oversized entries bypass the bounded cache but must still render correctly.
	app.fragments = newFragmentCache(1)
	check(base)
}
