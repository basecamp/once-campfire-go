package presentation

import (
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/useragent"
	"html/template"
	"testing"
)

// A complete-template fixture supplies the independent byte reference, including
// non-rendered user fields excluded by the production layout projection.
type shellFixture struct {
	User                                                                              database.User
	Room                                                                              database.Room
	Account                                                                           database.Account
	Platform                                                                          useragent.Platform
	Title, BodyClass, Screen, Origin, Stream, VAPIDPublicKey, Notice, Error, LoadedAt string
	CustomStyles, MessagesHTML                                                        template.HTML
	Frame, Chat, Reload, Invitation                                                   bool
	Query                                                                             string
	SearchResultCount                                                                 int
	RecentSearches                                                                    []string
	ReturnRoom                                                                        int64
	SidebarRooms                                                                      []RoomView
	Placeholders                                                                      []database.RoomParticipant
	CanCreateRooms                                                                    bool
	RoomsStream, UserRoomsStream                                                      string
	SidebarHTML                                                                       template.HTML
}

func (p shellFixture) layout() LayoutInput {
	return LayoutInput{User: LayoutUser{ID: p.User.ID, Name: p.User.Name, Bio: p.User.Bio, UpdatedAt: p.User.UpdatedAt, Role: p.User.Role}, Room: p.Room, Account: p.Account, Platform: p.Platform, Title: p.Title, BodyClass: p.BodyClass, Screen: p.Screen, Origin: p.Origin, Stream: p.Stream, VAPIDPublicKey: p.VAPIDPublicKey, Notice: p.Notice, Error: p.Error, CustomStyles: p.CustomStyles, Frame: p.Frame, Chat: p.Chat, Reload: p.Reload, Invitation: p.Invitation}
}
func (p shellFixture) sidebar() SidebarInput {
	return SidebarInput{User: p.User.Participant(), SidebarRooms: p.SidebarRooms, Placeholders: p.Placeholders, CanCreateRooms: p.CanCreateRooms, RoomsStream: p.RoomsStream, UserRoomsStream: p.UserRoomsStream}
}
func (p shellFixture) search() SearchInput {
	return SearchInput{LayoutInput: p.layout(), Query: p.Query, SearchResultCount: p.SearchResultCount, RecentSearches: p.RecentSearches, ReturnRoom: p.ReturnRoom}
}
func shellTestFragments(t *testing.T) (*Fragments, database.User) {
	t.Helper()
	secrets, err := rails.NewSecrets("test-only-secret")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := NewRenderer(secrets)
	if err != nil {
		t.Fatal(err)
	}
	return NewFragments(renderer, 32<<20), database.User{ID: 1, Name: "Owner", Role: 1}
}
