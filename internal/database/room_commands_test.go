package database

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestRoomCreateReturnsCommittedRecordWithoutReader(t *testing.T) {
	d := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actor, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	for range d.Read.Stats().MaxOpenConnections {
		conn, err := d.Read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	type result struct {
		room Room
		err  error
	}
	done := make(chan result, 1)
	go func() {
		room, err := d.CreateRoom(ctx, actor.ID, "Rooms::Closed", &sql.NullString{String: "Committed", Valid: true}, []int64{actor.ID})
		done <- result{room, err}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var id int64
	for {
		if err := d.Write.QueryRowContext(ctx, "SELECT coalesce(max(id),0) FROM rooms WHERE name='Committed'").Scan(&id); err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		if id != 0 {
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
	if got.err != nil || got.room.ID != id || got.room.CreatorID != actor.ID || got.room.Name != "Committed" || got.room.Type != "Rooms::Closed" || got.room.UpdatedAt.IsZero() {
		t.Fatalf("committed room: %+v %v", got.room, got.err)
	}
}
