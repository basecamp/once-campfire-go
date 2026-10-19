package fastdb

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"runtime"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// TestSessionActiveCgoBudget pins the warm-hit cgo transition budget of the
// auth fast path's joined read. Before Stmt.StepRow the read crossed into C 29
// times on the parity seed (clear_bindings, bind_text, step, 24 column
// accessor crossings for the ten columns, and reset); capturing the row in one
// transition leaves four: clear_bindings, bind_text, step_row and the release
// reset.
//
// The count is data-independent — an empty or NULL text column no longer
// changes it, because the capture does not depend on which columns the decode
// reads — so the same four hold on the synthetic fallback fixture. The
// untouched live path is the control: SessionUser, which still decodes through
// Rows, stays at 26 on the seed.
func TestSessionActiveCgoBudget(t *testing.T) {
	path := fixtureDB(t)
	d, err := database.Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	var token string
	if err := d.Read.QueryRowContext(ctx, "SELECT token FROM sessions LIMIT 1").Scan(&token); err != nil {
		t.Fatal(err)
	}

	c, err := OpenReadOnly(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var user User
	if _, err := c.SessionActive(&user, token); err != nil {
		t.Fatal(err)
	}
	if user.ID == 0 {
		t.Fatal("SessionActive decoded no user")
	}

	before := runtime.NumCgoCall()
	if _, err := c.SessionActive(&user, token); err != nil {
		t.Fatal(err)
	}
	if got := runtime.NumCgoCall() - before; got != 4 {
		t.Fatalf("warm SessionActive crossed into C %d times, want 4 (uncaptured: 29)", got)
	}
}

// TestSessionActiveMatchesSessionUser holds the captured-row decode to the
// live decode on every fixture session: the joined read must return exactly
// the user SessionUser returns, so the fast path's shortcut cannot drift from
// the lane the differential tests pin against database/sql.
func TestSessionActiveMatchesSessionUser(t *testing.T) {
	path := fixtureDB(t)
	d, err := database.Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	c, err := OpenReadOnly(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	rows, err := d.Read.QueryContext(ctx, "SELECT token FROM sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sessions := 0
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			t.Fatal(err)
		}
		sessions++
		var live, captured User
		if err := c.SessionUser(&live, token); err != nil {
			t.Fatalf("SessionUser(%q): %v", token, err)
		}
		if _, err := c.SessionActive(&captured, token); err != nil {
			t.Fatalf("SessionActive(%q): %v", token, err)
		}
		if !reflect.DeepEqual(captured.record(), live.record()) {
			t.Errorf("SessionActive(%q) decoded %+v, SessionUser decoded %+v", token, captured.record(), live.record())
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if sessions == 0 {
		t.Fatal("fixture has no sessions")
	}

	// The error paths must match too: a missing token is ErrNoRows with a zero
	// destination on both.
	var live, captured User
	if err := c.SessionUser(&live, "no-such-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SessionUser on a missing token: %v, want ErrNoRows", err)
	}
	if _, err := c.SessionActive(&captured, "no-such-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SessionActive on a missing token: %v, want ErrNoRows", err)
	}
	if !reflect.DeepEqual(captured.record(), live.record()) {
		t.Errorf("missing token: SessionActive %+v, SessionUser %+v", captured.record(), live.record())
	}
}
