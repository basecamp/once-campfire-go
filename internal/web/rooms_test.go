package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestRoomMutationRechecksCapturedAdministrator(t *testing.T) {
	app, _, _, captured := testApp(t)
	ctx := context.Background()
	creator, err := app.DB.CreateUser(ctx, captured.ID, database.UserInput{Name: "Creator", Email: "creator@test", Password: "digest", Bio: "", Role: 0, Webhook: nil})
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(ctx, creator.ID, "Rooms::Open", &sql.NullString{String: "Protected", Valid: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Authentication observed an administrator, but demotion commits before the
	// command. The still-valid membership cannot carry stale administrative rights.
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET role=0 WHERE id=?", captured.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.ExecContext(ctx, `UPDATE accounts SET settings='{"restrict_room_creation_to_administrators":true}'`); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		path := fmt.Sprintf("/rooms/opens/%d", room.ID)
		if method == "POST" {
			path = "/rooms/opens"
		}
		r := httptest.NewRequest(method, path, strings.NewReader("room%5Bname%5D=changed"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if method != "POST" {
			r.SetPathValue("id", fmt.Sprint(room.ID))
		}
		if err = parseRequestForm(r); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		if method != "DELETE" {
			app.saveRoom(w, r, captured)
		} else {
			app.deleteRoom(w, r, captured)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s accepted stale administrator: %d", method, w.Code)
		}
	}
	stored, err := app.DB.FindRoom(ctx, room.ID)
	if err != nil || stored.Name != "Protected" {
		t.Fatalf("unauthorized mutation: %+v %v", stored, err)
	}
}

func TestInvolvementRechecksCapturedActiveUser(t *testing.T) {
	app, _, _, owner := testApp(t)
	ctx := context.Background()
	member, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Member", Email: "member@test", Password: "unused", Role: 0})
	if err != nil {
		t.Fatal(err)
	}
	// Direct memberships survive deactivation for the remaining participants.
	room, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Direct", nil, []int64{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	before, err := app.DB.RoomInvolvement(ctx, member.ID, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.DB.DeactivateUser(ctx, owner.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	// Authentication captured an active user before deactivation committed.
	r := httptest.NewRequest("PUT", fmt.Sprintf("/rooms/%d/involvement", room.ID), strings.NewReader("involvement=invisible"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", fmt.Sprint(room.ID))
	if err = r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.involvement(w, r, member.User)
	if w.Code != http.StatusNotFound {
		t.Fatalf("captured inactive user changed involvement: %d", w.Code)
	}
	after, err := app.DB.RoomInvolvement(ctx, member.ID, room.ID)
	if err != nil || after != before {
		t.Fatalf("rejected command changed involvement: %q => %q, %v", before.Value, after.Value, err)
	}
}

func TestDirectRoomSettingsRetainInactiveParticipants(t *testing.T) {
	for _, status := range []string{"active", "deactivated", "banned"} {
		t.Run(status, func(t *testing.T) {
			app, server, cookie, owner := testApp(t)
			ctx := context.Background()
			member, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Retained Participant", Email: "participant@test", Password: "unused", Bio: "", Role: 0, Webhook: nil})
			if err != nil {
				t.Fatal(err)
			}
			room, err := app.DB.CreateRoom(ctx, owner.ID, "Rooms::Direct", nil, []int64{member.ID})
			if err != nil {
				t.Fatal(err)
			}
			switch status {
			case "deactivated":
				err = app.DB.DeactivateUser(ctx, owner.ID, member.ID)
			case "banned":
				err = app.DB.BanUser(ctx, owner.ID, member.ID, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			response, body := perform(
				t,
				server,
				"GET",
				fmt.Sprintf("/rooms/directs/%d/edit", room.ID),
				"",
				nil,
				cookie,
			)
			if response.StatusCode != http.StatusOK ||
				!strings.Contains(string(body), fmt.Sprintf(`href="/users/%d"`, member.ID)) ||
				!strings.Contains(string(body), "<strong>Retained Participant</strong>") {
				t.Fatalf("ping settings lost %s participant: %s", status, response.Status)
			}
			if strings.Contains(string(body), fmt.Sprintf(`href="/users/%d"`, owner.ID)) {
				t.Fatal("two-person ping settings included the current user")
			}
		})
	}
}

func TestInvolvementDocumentAndFrame(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	room, err := app.DB.CreateRoom(
		context.Background(),
		owner.ID,
		"Rooms::Open",
		&sql.NullString{String: "Notifications", Valid: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, framed := range []bool{false, true} {
		request, err := http.NewRequest(
			"GET",
			fmt.Sprintf("%s/rooms/%d/involvement", server.URL, room.ID),
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(cookie)
		if framed {
			request.Header.Set("Turbo-Frame", room.DOM("involvement"))
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("involvement GET: %s %v", response.Status, err)
		}
		text := string(body)
		if !strings.Contains(text, "<html>") || !strings.Contains(text, "</html>") ||
			strings.Count(text, `id="`+room.DOM("involvement")+`"`) != 1 {
			t.Fatal("involvement lost document or unique frame structure")
		}
		if strings.Contains(text, `type="importmap"`) == framed ||
			strings.Contains(text, `rel="stylesheet"`) == framed {
			t.Fatal("involvement used the wrong application/frame layout")
		}
	}
	response, body := perform(t, server, "GET", "/users/me/profile", "", nil, cookie)
	if response.StatusCode != http.StatusOK || strings.Count(string(body), "<html>") != 1 {
		t.Fatal("embedded involvement introduced a nested document")
	}
}

func TestInvolvementCanBeCleared(t *testing.T) {
	for _, form := range []string{"", "involvement=", "involvement=++"} {
		app, server, cookie, owner := testApp(t)
		ctx := context.Background()
		room, err := app.DB.CreateRoom(
			ctx,
			owner.ID,
			"Rooms::Open",
			&sql.NullString{String: "Nullable", Valid: true},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/rooms/%d/involvement", room.ID)
		response, _ := perform(
			t,
			server,
			"PUT",
			path,
			"application/x-www-form-urlencoded",
			strings.NewReader(form),
			cookie,
		)
		if response.StatusCode != http.StatusFound {
			t.Fatalf("%q: %s", form, response.Status)
		}
		var stored sql.NullString
		if err := app.DB.Read.QueryRowContext(ctx, "SELECT involvement FROM memberships WHERE room_id=? AND user_id=?", room.ID, owner.ID).Scan(&stored); err != nil ||
			stored.Valid {
			t.Fatalf("%q: stored %+v, error %v", form, stored, err)
		}
		if value, err := app.DB.RoomInvolvement(ctx, owner.ID, room.ID); err != nil || value.Value != "" {
			t.Fatalf("nullable involvement: %q %v", value.Value, err)
		}
	}
}

func TestRoomUpdatesPreserveOmittedName(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	room, err := app.DB.CreateRoom(
		ctx,
		owner.ID,
		"Rooms::Open",
		&sql.NullString{String: "Preserved", Valid: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, contentType, body, kind string
		name                            sql.NullString
	}{
		{"PATCH", "application/x-www-form-urlencoded", "room[ignored]=value", "Rooms::Open", sql.NullString{String: "Preserved", Valid: true}},
		{"PUT", "application/json", `{"room":{"ignored":true}}`, "Rooms::Open", sql.NullString{String: "Preserved", Valid: true}},
		{"POST", "application/x-www-form-urlencoded", "_method=patch&room[name]=must-not-overwrite", "Rooms::Open", sql.NullString{String: "Preserved", Valid: true}},
		{"PATCH", "application/x-www-form-urlencoded", fmt.Sprintf("room[ignored]=value&user_ids[]=%d", owner.ID), "Rooms::Closed", sql.NullString{String: "Preserved", Valid: true}},
		{"PATCH", "application/x-www-form-urlencoded", "room[name]=", "Rooms::Open", sql.NullString{Valid: true}},
		{"PATCH", "application/json", `{"room":{"name":null}}`, "Rooms::Open", sql.NullString{}},
		{"PUT", "application/json", `{"room":{"ignored":true}}`, "Rooms::Open", sql.NullString{}},
	} {
		namespace := "opens"
		if tc.kind == "Rooms::Closed" {
			namespace = "closeds"
		}
		before, err := app.DB.FindRoom(ctx, room.ID)
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/rooms/%s/%d", namespace, room.ID)
		if tc.method == "POST" {
			path += "?room[ignored]=query"
		}
		response, body := perform(
			t,
			server,
			tc.method,
			path,
			tc.contentType,
			strings.NewReader(tc.body),
			cookie,
		)
		if response.StatusCode != http.StatusFound {
			t.Fatalf("%s: %s %s", tc.body, response.Status, body)
		}
		var stored sql.NullString
		if err := app.DB.Read.QueryRowContext(ctx, "SELECT name FROM rooms WHERE id=?", room.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		after, err := app.DB.FindRoom(ctx, room.ID)
		if err != nil || stored != tc.name || after.Type != tc.kind {
			t.Errorf(
				"%s: stored name %+v/%+v, room %+v, error %v",
				tc.body,
				stored,
				tc.name,
				after,
				err,
			)
		}
		if strings.Contains(tc.body, "ignored") && before.Type == after.Type &&
			!before.UpdatedAt.Equal(after.UpdatedAt) {
			t.Errorf(
				"omitted name changed room freshness: %v -> %v",
				before.UpdatedAt,
				after.UpdatedAt,
			)
		}
	}
}

func TestRoomUpdatesRejectMissingRoot(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	room, err := app.DB.CreateRoom(
		ctx,
		owner.ID,
		"Rooms::Open",
		&sql.NullString{String: "Protected", Valid: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	// Compare persisted snapshots, not the creation receipt's higher-precision clock.
	room, err = app.DB.FindRoom(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := app.DB.RoomParticipants(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"opens", "closeds"} {
		for _, tc := range []struct{ contentType, body string }{
			{"application/x-www-form-urlencoded", ""},
			{"application/json", `{"room":{}}`},
		} {
			response, _ := perform(
				t,
				server,
				"PATCH",
				fmt.Sprintf("/rooms/%s/%d", namespace, room.ID),
				tc.contentType,
				strings.NewReader(tc.body),
				cookie,
			)
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("%s missing root: %s", namespace, response.Status)
			}
			stored, err := app.DB.FindRoom(ctx, room.ID)
			if err != nil || stored != room {
				t.Errorf("missing root changed room: %+v, %v", stored, err)
			}
			after, err := app.DB.RoomParticipants(ctx, room.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("missing root changed membership: %+v, %v", after, err)
			}
		}
	}
}

func TestRoomUpdatesRedirectWhenMembershipIsMissing(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	member, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Member", Email: "member@test", Password: "digest", Bio: "", Role: 0, Webhook: nil})
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(
		ctx,
		owner.ID,
		"Rooms::Open",
		&sql.NullString{String: "Protected", Valid: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.UpdateRoom(ctx, owner.ID, room.ID, "Rooms::Closed", nil, []int64{member.ID}); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"opens", "closeds"} {
		for _, id := range []int64{room.ID, room.ID + 1} {
			path := fmt.Sprintf("/rooms/%s/%d", namespace, id)
			response, body := perform(t, server, "PATCH", path, "application/x-www-form-urlencoded",
				strings.NewReader("room%5Bname%5D=forbidden"), cookie)
			if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/" {
				t.Errorf("inaccessible update %s: %s %s", path, response.Status, body)
			}
		}
	}
	stored, err := app.DB.FindRoom(ctx, room.ID)
	if err != nil || stored.Name != room.Name || stored.Type != "Rooms::Closed" {
		t.Fatalf("inaccessible updates changed the room: %+v %v", stored, err)
	}
}
