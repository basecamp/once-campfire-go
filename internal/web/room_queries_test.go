package web

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestClosedRoomFormKeepsSelectionAndActiveCandidates(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	create := func(name string, role int) database.User {
		t.Helper()
		commit, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: name, Email: name + "@test", Password: "unused", Bio: "Profile note", Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return commit.User
	}
	alpha, zulu, bot, inactive := create("Alpha", 0), create("Zulu", 0), create("BetaBot", 2), create("Inactive", 0)
	room, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Closed", &sql.NullString{String: "Selected room", Valid: true}, []int64{owner.ID, zulu.ID, inactive.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Write.ExecContext(ctx, "UPDATE users SET status=1 WHERE id=?", inactive.ID); err != nil {
		t.Fatal(err)
	}
	data, err := app.RoomQueries.Form(ctx, application.RoomFormRequest{UserID: owner.ID, Role: owner.Role, RoomID: room.ID, Kind: room.Type})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, choice := range data.Users {
		ids = append(ids, choice.ID)
		if choice.Selected != (choice.ID == owner.ID || choice.ID == zulu.ID) {
			t.Fatalf("lost membership selection: %+v", choice)
		}
		if choice.ID == zulu.ID && (choice.Title() != zulu.Title() || choice.UpdatedAt != zulu.UpdatedAt) {
			t.Fatalf("lost owned profile display: %+v", choice)
		}
	}
	if !reflect.DeepEqual(ids, []int64{owner.ID, zulu.ID, alpha.ID, bot.ID}) || data.Divider != 2 || !data.CanAdminister {
		t.Fatalf("closed-room ordering/controls: %+v", data)
	}
	response, body := perform(t, server, "GET", fmt.Sprintf("/rooms/closeds/%d/edit", room.ID), "", nil, cookie)
	if response.StatusCode != 200 {
		t.Fatalf("form: %s", response.Status)
	}
	text := string(body)
	for _, choice := range data.Users {
		input := fmt.Sprintf(`name="user_ids[]" value="%d" class="switch__input" `, choice.ID)
		if choice.Selected {
			input += "checked"
		}
		input += ">"
		if !strings.Contains(text, input) {
			t.Fatalf("membership switch missing: %s", input)
		}
	}
	if strings.Contains(text, "Inactive") {
		t.Fatal("shared-room choices included inactive user")
	}
	// A namespace switch proposes a different room type without losing its members.
	switched, err := app.RoomQueries.Form(ctx, application.RoomFormRequest{UserID: owner.ID, Role: owner.Role, RoomID: room.ID, Kind: "Rooms::Open"})
	if err != nil || switched.Room.Type != "Rooms::Open" || switched.Divider != 0 {
		t.Fatalf("type switch: %+v %v", switched, err)
	}
	self, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Direct", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := app.RoomQueries.Form(ctx, application.RoomFormRequest{UserID: owner.ID, Role: owner.Role, RoomID: self.ID, Kind: self.Type})
	if err != nil || len(direct.Users) != 1 || direct.Users[0].ID != owner.ID {
		t.Fatalf("self-ping form lost viewer: %+v %v", direct, err)
	}
}
