package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMessageReferencesMatchPagination(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	user, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0].ID
	base := time.Date(2026, 10, 5, 12, 0, 0, 123456000, time.UTC)
	var records []Message
	for i := 0; i < 46; i++ {
		d.Now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		message, err := d.CreateMessage(ctx, user.ID, room, "", "body", "body")
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, message)
	}
	for _, page := range []struct {
		room, anchor int64
		direction    string
	}{
		{room, 0, "around"},
		{room, records[42].ID, "before"},
		{room, records[2].ID, "before"},
		{room, records[22].ID, "around"},
		{room, records[22].ID, "after"},
		{room, -1, "before"},
		{room + 1, records[22].ID, "before"},
	} {
		want, err := d.MessagePage(ctx, page.room, page.anchor, page.direction)
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.MessagePageReferences(ctx, page.room, page.anchor, page.direction)
		if err != nil || len(got) != len(want) {
			t.Fatalf("page %+v: got %d, want %d: %v", page, len(got), len(want), err)
		}
		for i := range want {
			if got[i].ID != want[i].ID || got[i].RoomID != want[i].RoomID || !got[i].UpdatedAt.Equal(want[i].UpdatedAt) {
				t.Fatalf("page %+v: reference %d differs", page, i)
			}
		}
	}
	// Reads must observe external commits and the scanner's existing formats.
	for _, value := range []any{"2026-10-05 13:00:00.123456+01:00", []byte("2026-10-05T12:00:00.123456Z"), "invalid"} {
		if _, err := d.Write.ExecContext(ctx, "UPDATE messages SET updated_at=? WHERE id=?", value, records[45].ID); err != nil {
			t.Fatal(err)
		}
		want, expectedErr := d.Messages(ctx, room, 0)
		got, err := d.MessagePageReferences(ctx, room, 0, "around")
		if (err != nil) != (expectedErr != nil) {
			t.Fatalf("timestamp %q: errors differ: %v / %v", value, err, expectedErr)
		}
		if err == nil && !got[len(got)-1].UpdatedAt.Equal(want[len(want)-1].UpdatedAt) {
			t.Fatal("updated timestamp was stale or lost precision")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.MessagePageReferences(cancelled, room, 0, "around"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
}
