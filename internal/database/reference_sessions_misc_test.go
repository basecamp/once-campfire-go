package database

import (
	"context"
	"testing"
)

func TestFirstRunCreatesTheAccountOnce(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	administrator, err := d.FirstRunCreate(ctx, "Ada", "ada@example.com", "digest", nil)
	if err != nil {
		t.Fatal(err)
	}
	if administrator.Name != "Ada" || administrator.Email != "ada@example.com" || administrator.Role != 1 || administrator.Status != 0 {
		t.Fatal(administrator)
	}
	var room, members int64
	var involvement string
	if err := d.Read.QueryRowContext(ctx, `SELECT r.id, m.involvement, (SELECT count(*) FROM memberships) FROM rooms r JOIN memberships m ON m.room_id = r.id WHERE r.name = 'All Talk' AND r.type = 'Rooms::Open' AND m.user_id = ?`, administrator.ID).
		Scan(&room, &involvement, &members); err != nil {
		t.Fatal(err)
	}
	if involvement != "mentions" || members != 1 {
		t.Fatal(involvement, members)
	}
	_, err = d.FirstRunCreate(ctx, "Bob", "bob@example.com", "digest", nil)
	if !IsRecordNotUnique(err) {
		t.Fatal("a second first run", err)
	}
	if users, err := d.UserCount(ctx); err != nil || users != 1 {
		t.Fatal(users, err)
	}
}

func TestSessionTokensAreBase58(t *testing.T) {
	for range 100 {
		token := base58(24)
		if len(token) != 24 {
			t.Fatal(token)
		}
		for _, c := range token {
			if c == '0' || c == 'O' || c == 'I' || c == 'l' || !(c >= '1' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
				t.Fatal(token)
			}
		}
	}
}
