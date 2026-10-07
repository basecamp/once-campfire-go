package web

import (
	"html/template"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

// Only immutable surrounding template bytes are retained. Eviction cannot change
// Parts already selected for an outstanding response.
type templateShell struct {
	parts []responsebody.Part
	bytes int
}

// Layouts need profile and role data, not credentials or account status.
// A separate type keeps those non-rendered fields out of both template input
// and cache identity, rather than maintaining a separate hash-only projection.
type layoutUser struct {
	ID        int64
	Name, Bio string
	UpdatedAt time.Time
	Role      int
}

func (u layoutUser) Title() string {
	return (database.User{Name: u.Name, Bio: u.Bio}).Title()
}

// The same bounded input owns both the template and its cache identity. New
// layout-template dependencies must enter this type, not an unrelated page field.
type layoutShellPage struct {
	User                            layoutUser
	Room                            database.Room
	Account                         database.Account
	Platform                        useragent.Platform
	Title, BodyClass, Screen        string
	Origin, Stream, VAPIDPublicKey  string
	Notice, Error, LoadedAt         string
	CustomStyles, MessagesHTML      template.HTML
	Messages                        []messageView
	Frame, Chat, Reload, Invitation bool
}

func shellPage(p page) layoutShellPage {
	room := p.Room
	// Message activity touches the room, but surrounding HTML does not render
	// that timestamp. The fresh refresh cursor is inserted by roomParts.
	room.UpdatedAt = time.Time{}
	return layoutShellPage{
		User: layoutUser{ID: p.User.ID, Name: p.User.Name, Bio: p.User.Bio, UpdatedAt: p.User.UpdatedAt, Role: p.User.Role},
		Room: room, Account: p.Account, Platform: p.Platform,
		Title: p.Title, BodyClass: p.BodyClass, Screen: p.Screen,
		Origin: p.Origin, Stream: p.Stream, VAPIDPublicKey: p.VAPIDPublicKey,
		Notice: p.Notice, Error: p.Error, CustomStyles: p.CustomStyles,
		Frame: p.Frame, Chat: p.Chat, Reload: p.Reload, Invitation: p.Invitation,
	}
}
