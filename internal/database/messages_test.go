package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestMessageUpdateReturnsCommittedRecordWithoutReader(t *testing.T) {
	d := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := d.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", "before"))
	if err != nil {
		t.Fatal(err)
	}
	// A successful command must not need another pooled read to reconstruct its
	// result. Pin every reader, observe the commit through the writer, then cancel.
	for range d.Read.Stats().MaxOpenConnections {
		conn, err := d.Read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	type result struct {
		message Message
		err     error
	}
	done := make(chan result, 1)
	go func() {
		m, err := d.UpdateMessage(ctx, user.ID, message.ID, messageInput("", "after"))
		done <- result{m.Message, err}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var body string
		if err := d.Write.QueryRowContext(ctx, "SELECT body FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?", message.ID).Scan(&body); err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		if body == "after" {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			<-done
			t.Fatal("command did not commit", ctx.Err())
		}
	}
	cancel()
	got := <-done
	if got.err != nil || got.message.ID != message.ID || got.message.Body != "after" {
		t.Fatalf("committed result: %+v %v", got.message, got.err)
	}
}

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
		message, err := d.CreateMessage(ctx, user.ID, room, messageInput("", "body"))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, message.Message)
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
	} {
		want, err := messageRecords(d, ctx, page.room, page.anchor, page.direction)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pageReferences(d, ctx, page.room, page.anchor, page.direction)
		if err != nil || len(got) != len(want) {
			t.Fatalf("page %+v: got %d, want %d: %v", page, len(got), len(want), err)
		}
		for i := range want {
			if got[i].ID != want[i].ID || got[i].RoomID != want[i].RoomID ||
				!got[i].UpdatedAt.Equal(want[i].UpdatedAt) {
				t.Fatalf("page %+v: reference %d differs", page, i)
			}
		}
	}
	loads := []func(context.Context, int64, int64, string) (int, error){
		func(ctx context.Context, room, anchor int64, direction string) (int, error) {
			rows, err := messageRecords(d, ctx, room, anchor, direction)
			return len(rows), err
		},
		func(ctx context.Context, room, anchor int64, direction string) (int, error) {
			rows, err := pageReferences(d, ctx, room, anchor, direction)
			return len(rows), err
		},
	}
	for _, direction := range []string{"before", "after", "around"} {
		for _, anchor := range []int64{-1, records[22].ID} {
			// A missing anchor or one belonging to another room is not an empty page.
			for _, load := range loads {
				if _, err := load(ctx, room+1, anchor, direction); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("foreign/missing %s cursor %d: %v", direction, anchor, err)
				}
			}
		}
	}
	// An existing oldest message has a valid, empty preceding page.
	for _, load := range loads {
		if messages, err := load(ctx, room, records[0].ID, "before"); err != nil ||
			messages != 0 {
			t.Fatalf("oldest cursor: %d messages, %v", messages, err)
		}
	}
	// Reads must observe external commits and the scanner's existing formats.
	for _, value := range []any{"2026-10-05 13:00:00.123456+01:00", []byte("2026-10-05T12:00:00.123456Z"), "invalid"} {
		if _, err := d.Write.ExecContext(ctx, "UPDATE messages SET updated_at=? WHERE id=?", value, records[45].ID); err != nil {
			t.Fatal(err)
		}
		want, expectedErr := messageRecords(d, ctx, room, 0, "before")
		got, err := pageReferences(d, ctx, room, 0, "around")
		if (err != nil) != (expectedErr != nil) {
			t.Fatalf("timestamp %q: errors differ: %v / %v", value, err, expectedErr)
		}
		if err == nil && !got[len(got)-1].UpdatedAt.Equal(want[len(want)-1].UpdatedAt) {
			t.Fatal("updated timestamp was stale or lost precision")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := pageReferences(d, cancelled, room, 0, "around"); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancelled query: %v", err)
	}
}
