package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Login help consumes a nullable email, not credential/status/display timestamps.
// A narrow contact projection must keep the same active lowest-ID administrator.
func TestLoginHelpKeepsNullableEmailAndActiveAdministrator(t *testing.T) {
	app, server, _, owner := testApp(t)
	ctx := context.Background()
	other, err := app.DB.CreateUser(ctx, owner.ID, database.UserInput{Name: "Second administrator", Email: "second@test", Password: "digest", Role: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET email_address=NULL WHERE id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	check := func(name string) {
		t.Helper()
		response, body := perform(t, server, "GET", "/session/new", "", nil, nil)
		if response.StatusCode != 200 || !strings.Contains(string(body), fmt.Sprintf(`title="Email %s"`, name)) {
			t.Fatalf("login lost help contact: %s", response.Status)
		}
	}
	check(owner.Name)
	if _, err = app.DB.Write.ExecContext(ctx, "UPDATE users SET status=2 WHERE id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	check(other.Name)
}
