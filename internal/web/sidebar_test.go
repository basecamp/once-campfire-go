package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
)

func TestDirectRoomDisplayUsesAllNamesAndFirstFourAvatars(t *testing.T) {
	app, _, _, viewer := testApp(t)
	room := database.Room{ID: 40, Type: "Rooms::Direct"}
	members := []database.RoomParticipant{viewer.Participant()}
	for i := 1; i <= 6; i++ {
		members = append(
			members,
			database.RoomParticipant{ID: viewer.ID + int64(i), Name: fmt.Sprintf("Person %d", i)},
		)
	}
	view := presentation.DisplayRoom(room, members, viewer.Participant())
	if len(view.Members) != 6 || view.Label() != "P1, P2, P3, P4, P5, and P6" ||
		view.Name != "Person 1, Person 2, Person 3, Person 4, Person 5, and Person 6" {
		t.Fatalf("direct display lost a retained participant: %+v", view)
	}
	body, err := app.Presentation.Markup("sidebar-direct", view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(body, `width="20"`) != 4 || !strings.Contains(body, view.Label()) {
		t.Fatal("group must render four avatars but initials for every participant")
	}
	self := presentation.DisplayRoom(room, members[:1], viewer.Participant())
	if len(self.Members) != 1 || self.Members[0] != viewer.Participant() ||
		self.Name != viewer.Name {
		t.Fatalf("self-ping lost its viewing participant: %+v", self)
	}
}

func TestSidebarDocumentAndFrameKeepLayoutFresh(t *testing.T) {
	app, server, cookie, user := testApp(t)
	get := func(frame bool) string {
		t.Helper()
		r, err := http.NewRequest("GET", server.URL+"/users/me/sidebar", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie)
		if frame {
			r.Header.Set("Turbo-Frame", "user_sidebar")
		}
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("sidebar response: %d %v", res.StatusCode, err)
		}
		text := string(body)
		for _, required := range []string{"<html>", "<head>", "<body", "</body>", "</html>", `<turbo-frame id="user_sidebar"`} {
			if !strings.Contains(text, required) {
				t.Fatalf("sidebar missing document/frame structure %q", required)
			}
		}
		if strings.Count(text, `<turbo-frame id="user_sidebar"`) != 1 {
			t.Fatal("sidebar duplicated its frame")
		}
		return text
	}
	for range 2 {
		document := get(false)
		if !strings.HasPrefix(document, "<!DOCTYPE html>") ||
			!strings.Contains(document, `name="current-user-id"`) ||
			!strings.Contains(document, `class=" admin"`) {
			t.Fatal("ordinary sidebar GET lost the application layout")
		}
		frame := get(true)
		if strings.Contains(frame, "<!DOCTYPE html>") ||
			strings.Contains(frame, `name="current-user-id"`) {
			t.Fatal("Turbo-Frame GET used the application layout")
		}
	}
	// These changes do not alter the cached sidebar frame or its key: the account
	// permits room creation for both roles. Surrounding observations stay fresh.
	originalFrame := get(true)
	if _, err := app.DB.Write.ExecContext(context.Background(), "UPDATE users SET role=0 WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	styles := "body{--sidebar-fresh:1}"
	if _, err := app.DB.Write.ExecContext(context.Background(), "UPDATE accounts SET custom_styles=?,updated_at=?", styles, database.Stamp(app.DB.Now())); err != nil {
		t.Fatal(err)
	}
	document := get(false)
	if !strings.Contains(document, styles) || strings.Contains(document, `class=" admin"`) {
		t.Fatal("cached sidebar frame hid fresh role or custom styles")
	}
	frame := get(true)
	if strings.Contains(frame, styles) {
		t.Fatal("cached application layout leaked into the Turbo-Frame response")
	}
	if frame != originalFrame {
		t.Fatal("layout-only changes altered the sidebar frame")
	}
	if _, err := app.DB.Write.ExecContext(context.Background(), "UPDATE users SET name=? WHERE id=?", "Fresh Profile Name", user.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(get(false), "Fresh Profile Name") {
		t.Fatal("sidebar layout hid fresh profile data")
	}
}
