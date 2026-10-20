package application

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestAccountPeopleKeepsVisibilityAndPagination(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "account.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	owner, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	now := database.Stamp(db.Now())
	// The active set spans the 500-person stream boundary. The document has
	// historically retained all people, and counts before splitting administrators.
	_, err = db.Write.ExecContext(ctx, `WITH RECURSIVE people(n) AS (
 SELECT 1 UNION ALL SELECT n+1 FROM people WHERE n<500
) INSERT INTO users(name,email_address,role,status,created_at,updated_at)
SELECT printf('Person %03d',n),printf('person%d@test',n),0,0,?,? FROM people`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []database.UserInput{{Name: "Banned", Email: "banned@test", Role: 0}, {Name: "Inactive", Email: "inactive@test", Role: 0}, {Name: "Bot", Role: 2}} {
		result, err := db.CreateUser(ctx, owner.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		status := 0
		if input.Name == "Banned" {
			status = 2
		} else if input.Name == "Inactive" {
			status = 1
		}
		if _, err := db.Write.ExecContext(ctx, "UPDATE users SET status=? WHERE id=?", status, result.User.ID); err != nil {
			t.Fatal(err)
		}
	}
	q := AccountQueries{DB: db}
	admin, err := q.Settings(ctx, 1, 1)
	if err != nil || len(admin.Administrators) != 1 || admin.Administrators[0].ID != owner.ID || len(admin.Users) != 501 || admin.Users[0].Name != "Banned" || admin.Users[0].Status != 2 || admin.NextPage != 2 {
		t.Fatalf("administrator settings: %+v %v", admin, err)
	}
	member, err := q.Settings(ctx, 0, 1)
	if err != nil || len(member.Users) != 500 || len(member.Administrators) != 1 || member.NextPage != 2 {
		t.Fatalf("member settings: %+v %v", member, err)
	}
	for _, test := range []struct {
		number int64
		count  int
		next   int64
	}{{0, 500, 2}, {2, 1, 0}, {3, 0, 4}} {
		page, err := q.Members(ctx, test.number)
		if err != nil || len(page.Users) != test.count || page.NextPage != test.next {
			t.Fatalf("stream page %d: count=%d next=%d error=%v", test.number, len(page.Users), page.NextPage, err)
		}
	}
}
