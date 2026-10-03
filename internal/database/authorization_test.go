package database

import (
	"context"
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
	check := func(room int64, want int) {
		t.Helper()
		got, err := d.AuthorizedSessions(ctx, []string{a, b, "invalid"}, room)
		if err != nil || len(got) != want {
			t.Fatalf("authorization: %v %v; want %d", got, err, want)
		}
		for _, id := range got {
			if id != u.ID {
				t.Fatal("wrong identity")
			}
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
}
