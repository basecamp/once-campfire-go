package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestJoinUsesCurrentCapabilityAndIPBan(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := d.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.UpdateAccount(ctx, owner.ID, AccountInput{ResetJoin: true})
	if err != nil {
		t.Fatal(err)
	}
	input := UserInput{Name: "New user", Email: "new@test", Password: "digest"}
	if _, err = d.JoinUser(ctx, captured.JoinCode, "203.0.113.10", input); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rotated join capability accepted: %v", err)
	}
	if _, err = d.Write.ExecContext(ctx, "INSERT INTO bans(user_id,ip_address,created_at,updated_at) VALUES (?,?,?,?)", owner.ID, "203.0.113.10", Stamp(d.Now()), Stamp(d.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err = d.JoinUser(ctx, current.JoinCode, "203.0.113.10", input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("current IP ban ignored: %v", err)
	}
	var count int
	if err = d.Read.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
