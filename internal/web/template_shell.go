package web

import (
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"github.com/basecamp/once-campfire-go/internal/useragent"
	"html/template"
)

// Only immutable surrounding template bytes are retained. Eviction cannot change
// Parts already selected for an outstanding response.
type templateShell struct {
	parts []responsebody.Part
	bytes int
}

// The same bounded input owns both the template and its cache identity. New
// layout-template dependencies must enter this type, not an unrelated page field.
type layoutShellPage struct {
	User                            database.User
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
	return layoutShellPage{
		User: p.User, Room: p.Room, Account: p.Account, Platform: p.Platform,
		Title: p.Title, BodyClass: p.BodyClass, Screen: p.Screen,
		Origin: p.Origin, Stream: p.Stream, VAPIDPublicKey: p.VAPIDPublicKey,
		Notice: p.Notice, Error: p.Error, CustomStyles: p.CustomStyles,
		Frame: p.Frame, Chat: p.Chat, Reload: p.Reload, Invitation: p.Invitation,
	}
}
