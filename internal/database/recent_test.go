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
	first, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(first) != 3 {
		t.Fatal(len(first), err)
	}
	second, err := d.MessagePageReferences(ctx, room, 0, "around")
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
	afterCreate, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(afterCreate) != 4 || afterCreate[3].ID != created.ID {
		t.Fatalf("create did not extend the window: %+v %v", afterCreate, err)
	}
	hits, misses = d.PageCacheStats()
	if hits != 2 || misses != 1 {
		t.Fatalf("create should stay a hit, hits=%d misses=%d", hits, misses)
	}

	updated, err := d.UpdateMessage(ctx, user.ID, created.ID, "<p>edited</p>", "edited")
	if err != nil {
		t.Fatal(err)
	}
	afterUpdate, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || !afterUpdate[3].UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("update did not patch the window: %v %v", afterUpdate, err)
	}

	if err = d.DeleteMessage(ctx, user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil || len(afterDelete) != 3 {
		t.Fatal(len(afterDelete), err)
	}
	for _, message := range afterDelete {
		if message.ID == created.ID {
			t.Fatal("deleted message stayed in the window")
		}
	}
	hits, misses = d.PageCacheStats()
	if misses != 2 {
		t.Fatalf("delete should refill once, misses=%d", misses)
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
