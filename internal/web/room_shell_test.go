package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/splice"
	"html/template"
	"io"
	"testing"
	"time"
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
		parts, err := app.roomShell(p, fragmentEntry{html: p.MessagesHTML, digest: sha256.Sum256([]byte(p.MessagesHTML))})
		if err != nil {
			t.Fatal(err)
		}
		var actual, member bytes.Buffer
		writer := splice.NewWriter(&member, time.Time{})
		for _, part := range parts {
			actual.Write(part.Plain)
			writer.WritePiece(part.Piece)
		}
		if actual.String() != expected.String() {
			t.Fatal("cached room shell differs from uncached template")
		}
		writer.Close()
		reader, err := gzip.NewReader(&member)
		if err != nil {
			t.Fatal(err)
		}
		if spliced, err := io.ReadAll(reader); err != nil || string(spliced) != expected.String() {
			t.Fatal("spliced room shell differs from uncached template", err)
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
		"invitation":             func(p *page) { p.Invitation = true; p.JoinCode = "new-code" },
		"stream":                 func(p *page) { p.Stream = "new-stream" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { p := base; change(&p); check(p); check(base) })
	}
	// Oversized entries bypass the bounded cache but must still render correctly.
	app.fragments = newFragmentCache(1)
	check(base)
}
