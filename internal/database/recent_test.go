package database

import (
	"context"
	"testing"
)

func TestLatestWindowAndGenerations(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	user, err := d.Setup(ctx, "David", "david@example.test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil || len(rooms) != 1 {
		t.Fatal(rooms, err)
	}
	room := rooms[0].ID
	for i := 0; i < 3; i++ {
		if _, err = d.CreateMessage(ctx, user.ID, room, "", "<p>n</p>", "n"); err != nil {
			t.Fatal(err)
		}
	}
	d.ResetPageStats()
	first, _, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(first) != 3 {
		t.Fatal(len(first), err)
	}
	second, _, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(second) != 3 || second[2].ID != first[2].ID {
		t.Fatal(second, err)
	}
	hits, misses := d.PageCacheStats()
	if hits != 1 || misses != 1 {
		t.Fatalf("window stats hits=%d misses=%d", hits, misses)
	}

	created, err := d.CreateMessage(ctx, user.ID, room, "", "<p>fresh</p>", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	afterCreate, _, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(afterCreate) != 4 || afterCreate[3].ID != created.ID {
		t.Fatalf("create did not refill the window: %+v %v", afterCreate, err)
	}
	hits, misses = d.PageCacheStats()
	if hits != 1 || misses != 2 {
		t.Fatalf("create should refill from sqlite, hits=%d misses=%d", hits, misses)
	}

	updated, err := d.UpdateMessage(ctx, user.ID, created.ID, "<p>edited</p>", "edited")
	if err != nil {
		t.Fatal(err)
	}
	afterUpdate, _, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || !afterUpdate[3].UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("update did not refill the window: %v %v", afterUpdate, err)
	}

	if err = d.DeleteMessage(ctx, user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, _, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(afterDelete) != 3 {
		t.Fatal(len(afterDelete), err)
	}
	for _, message := range afterDelete {
		if message.ID == created.ID {
			t.Fatal("deleted message stayed in the window")
		}
	}
	hits, misses = d.PageCacheStats()
	if hits != 1 || misses != 4 {
		t.Fatalf("each local write should refill, hits=%d misses=%d", hits, misses)
	}

	name := "Renamed"
	beforeGen := d.ContentGeneration()
	if err = d.UpdateAccount(ctx, &name, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	account, err := d.Account(ctx)
	if err != nil || account.Name != "Renamed" {
		t.Fatalf("account cache kept the old name: %+v %v", account, err)
	}
	if d.ContentGeneration() == beforeGen {
		t.Fatal("account update did not bump the content generation")
	}

	userGen, content := d.UserGeneration(user.ID)
	if err = d.Presence(ctx, user.ID, room, "refresh"); err != nil {
		t.Fatal(err)
	}
	if again, againContent := d.UserGeneration(user.ID); again != userGen || againContent != content {
		t.Fatalf("refresh bumped generations: %d/%d -> %d/%d", userGen, content, again, againContent)
	}
	if err = d.Presence(ctx, user.ID, room, "present"); err != nil {
		t.Fatal(err)
	}
	if again, againContent := d.UserGeneration(user.ID); again != userGen+1 || againContent != content {
		t.Fatalf("present should bump only the user generation: %d/%d -> %d/%d", userGen, content, again, againContent)
	}
}

func TestUserMutationsBumpContentGeneration(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	user, err := d.Setup(ctx, "David", "david@example.test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	before := d.ContentGeneration()
	member, err := d.CreateUser(ctx, "Member", "member@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.ContentGeneration() == before {
		t.Fatal("CreateUser did not bump content generation")
	}
	before = d.ContentGeneration()
	if err = d.UpdateUser(ctx, member.ID, map[string]string{"name": "Renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	if d.ContentGeneration() == before {
		t.Fatal("UpdateUser did not bump content generation")
	}
	before = d.ContentGeneration()
	if err = d.DeactivateUser(ctx, member.ID); err != nil {
		t.Fatal(err)
	}
	if d.ContentGeneration() == before {
		t.Fatal("DeactivateUser did not bump content generation")
	}
	_ = user
}

func TestInvalidateAccountClearsHasLogo(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if _, err := d.Setup(ctx, "David", "david@example.test", "digest"); err != nil {
		t.Fatal(err)
	}
	first, err := d.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.storeAccount(Account{ID: first.ID, Name: first.Name, JoinCode: first.JoinCode, UpdatedAt: first.UpdatedAt, HasLogo: true, Settings: first.Settings})
	cached, err := d.Account(ctx)
	if err != nil || !cached.HasLogo {
		t.Fatalf("expected cached HasLogo: %+v %v", cached, err)
	}
	before := d.ContentGeneration()
	d.InvalidateAccount()
	if d.ContentGeneration() == before {
		t.Fatal("InvalidateAccount did not bump content generation")
	}
	again, err := d.Account(ctx)
	if err != nil || again.HasLogo {
		t.Fatalf("account cache kept HasLogo: %+v %v", again, err)
	}
}
