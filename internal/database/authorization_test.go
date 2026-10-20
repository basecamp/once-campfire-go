package database

import (
	"context"
	"errors"
	"testing"
)

func TestBatchedAuthorizationReadsCurrentSessionsAndMemberships(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	u, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.StartSession(ctx, u.ID, "one", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.StartSession(ctx, u.ID, "two", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the publication-owned keys, including stale permissions. Every check
	// must replace authority rather than retaining a previous successful result.
	got := map[string]int64{a: u.ID, b: u.ID, "invalid": u.ID}
	check := func(room int64, want int) {
		t.Helper()
		if err := d.AuthorizeSessions(ctx, got, room); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, id := range got {
			if id != 0 {
				if id != u.ID {
					t.Fatal("wrong identity")
				}
				count++
			}
		}
		if count != want || got["invalid"] != 0 {
			t.Fatalf("authorization: %v; want %d", got, want)
		}
	}
	check(rooms[0].ID, 2)
	if _, err = d.Write.ExecContext(ctx, "DELETE FROM sessions WHERE token=?", a); err != nil {
		t.Fatal(err)
	}
	check(rooms[0].ID, 1)
	if _, err = d.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id=?", u.ID, rooms[0].ID); err != nil {
		t.Fatal(err)
	}
	check(rooms[0].ID, 0)
	check(0, 1)
	if _, err = d.Write.ExecContext(ctx, "UPDATE users SET status=2 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	check(0, 0)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	got[a], got[b] = u.ID, u.ID
	if err := d.AuthorizeSessions(cancelled, got, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authorization: %v", err)
	}
	for _, id := range got {
		if id != 0 {
			t.Fatal("failed authorization retained permissions", got)
		}
	}
}
