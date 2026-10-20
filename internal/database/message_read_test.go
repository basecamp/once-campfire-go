package database

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestMessageReadRetainsMatchingBodyAndRelationships(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "snapshot author", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := d.CreateMessage(ctx, owner.ID, rooms[0].ID, messageInput("", "<p>snapshotneedle</p>"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := d.BeginMessageRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	refs, err := read.SearchReferences(ctx, owner.ID, "snapshotneedle")
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
	// These real commits occur between scoped selection and body/association loads.
	if _, err = d.UpdateMessage(ctx, owner.ID, message.ID, messageInput("", "<p>differentword</p>")); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "UPDATE users SET name='changed author' WHERE id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.ExecContext(ctx, "DELETE FROM memberships WHERE user_id=? AND room_id=?", owner.ID, rooms[0].ID); err != nil {
		t.Fatal(err)
	}
	records, err := read.Records(ctx, refs)
	if err != nil || len(records) != 1 || records[0].Body != "<p>snapshotneedle</p>" {
		t.Fatal("selection and matching body split", records, err)
	}
	data, _, err := read.Displays(ctx, records, nil)
	if err != nil || data[message.ID].Author == nil || data[message.ID].Author.Name != "snapshot author" {
		t.Fatal("associations escaped observation", data, err)
	}
	invalid := refs[0]
	invalid.RoomID++
	if _, err = read.Records(ctx, []MessageReference{invalid}); !errors.Is(err, ErrForbidden) {
		t.Fatal("accepted unselected reference", err)
	}
	if err = read.Finish(); err != nil {
		t.Fatal(err)
	}
	d.Read.SetMaxOpenConns(1)
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if refs, err = d.SearchReferences(deadline, owner.ID, "snapshotneedle"); err != nil || len(refs) != 0 {
		t.Fatal("snapshot not released or new scope stale", refs, err)
	}
}

func TestMessagePageKeepsStrictTimestampTies(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var ids []int64
	for _, seconds := range []int{0, 1, 1, 2} {
		d.Now = func() time.Time { return base.Add(time.Duration(seconds) * time.Second) }
		message, err := d.CreateMessage(ctx, owner.ID, rooms[0].ID, messageInput("", "body"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, message.ID)
	}
	for _, test := range []struct {
		direction string
		want      []int64
	}{
		{"before", []int64{ids[0]}},
		{"after", []int64{ids[3]}},
		{"around", []int64{ids[0], ids[1], ids[3]}},
	} {
		refs, err := pageReferences(d, ctx, rooms[0].ID, ids[1], test.direction)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]int64, len(refs))
		for i, ref := range refs {
			got[i] = ref.ID
		}
		if !slices.Equal(got, test.want) {
			t.Fatalf("%s: got %v, want %v", test.direction, got, test.want)
		}
	}
}
