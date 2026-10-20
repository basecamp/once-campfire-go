package presentation

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
type LayoutUser struct {
	ID        int64
	Name, Bio string
	UpdatedAt time.Time
	Role      int
}

func (u LayoutUser) Title() string {
	return (database.UserDisplay{Name: u.Name, Bio: u.Bio}).Title()
}

// The same bounded input owns both the template and its cache identity. New
// layout-template dependencies must enter this type, not an unrelated page field.
type LayoutInput struct {
	User                            LayoutUser
	Room                            database.Room
	Account                         database.Account
	Platform                        useragent.Platform
	Title, BodyClass, Screen        string
	Origin, Stream, VAPIDPublicKey  string
	Notice, Error                   string
	CustomStyles                    template.HTML
	Frame, Chat, Reload, Invitation bool
}

type layoutShellPage struct {
	LayoutInput
	MessagesHTML template.HTML
	LoadedAt     string
}

func shellPage(input LayoutInput) layoutShellPage {
	// Message activity is not a layout dependency. The current cursor is inserted separately.
	input.Room.UpdatedAt = time.Time{}
	return layoutShellPage{LayoutInput: input}
}
