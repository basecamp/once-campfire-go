package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestBotCatalogKeepsCredentialsAndHiddenSharedMemberships(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	create := func(name string, role int) database.User {
		t.Helper()
		user, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: name, Bio: "Useful updates", Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return user.User
	}
	bot := create("Zulu bot", 2)
	empty := create("Alpha empty bot", 2)
	inactive := create("Inactive bot", 2)
	if _, err := app.DB.Write.ExecContext(ctx, "UPDATE users SET status=2 WHERE id=?", inactive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id IN (?,?)", bot.ID, empty.ID); err != nil {
		t.Fatal(err)
	}
	var rooms []database.Room
	for _, name := range []string{"Zulu room", "Alpha room"} {
		room, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Closed", &sql.NullString{String: name, Valid: true}, []int64{owner.ID, bot.ID})
		if err != nil {
			t.Fatal(err)
		}
		rooms = append(rooms, room)
	}
	for i, involvement := range []any{"invisible", nil} {
		if _, err := app.DB.Write.ExecContext(ctx, "UPDATE memberships SET involvement=? WHERE user_id=? AND room_id=?", involvement, bot.ID, rooms[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Direct", nil, []int64{bot.ID}); err != nil {
		t.Fatal(err)
	}
	catalog, err := app.BotQueries.Catalog(ctx)
	if err != nil || len(catalog) != 2 {
		t.Fatalf("bot catalog: %+v %v", catalog, err)
	}
	if catalog[0].User.ID != empty.ID || len(catalog[0].Rooms) != 0 || catalog[1].User.ID != bot.ID || catalog[1].User.BotKey() != bot.BotKey() || catalog[1].User.Title() != bot.Title() {
		t.Fatalf("bot identity, order or credentials changed: %+v", catalog)
	}
	shared := catalog[1].Rooms
	if len(shared) != 2 || shared[0].ID != rooms[1].ID || shared[1].ID != rooms[0].ID {
		t.Fatalf("shared membership visibility/order changed: %+v", shared)
	}
	response, body := perform(t, server, "GET", "/account/bots", "", nil, cookie)
	text := string(body)
	if response.StatusCode != 200 || !strings.Contains(text, bot.BotKey()) || !strings.Contains(text, bot.Title()) || !strings.Contains(text, empty.Name) || strings.Contains(text, inactive.Name) || strings.Count(text, `aria-label="curl command for posting messages"`) != 2 {
		t.Fatalf("prepared bot instructions changed: %s %s", response.Status, body)
	}
}

func TestBotFormKeepsMissingAndIneligibleResponsePolicies(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	inactive, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Inactive bot", Role: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Write.ExecContext(ctx, "UPDATE users SET status=2 WHERE id=?", inactive.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{owner.ID, inactive.ID, 9999999} {
		request, err := http.NewRequest("GET", fmt.Sprintf("%s/account/bots/%d/edit", server.URL, id), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(cookie)
		request.Header.Set("Accept", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		want := "404 page not found\n"
		if id == 9999999 {
			want = `{"status":404,"error":"Not Found"}`
		}
		if response.StatusCode != 404 || string(body) != want {
			t.Fatalf("bot %d response changed: %s %s", id, response.Status, body)
		}
	}
}
