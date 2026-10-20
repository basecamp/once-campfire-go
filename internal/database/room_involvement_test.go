package database

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestInvolvementReceiptUsesWriterObservationWithoutReaders(t *testing.T) {
	d := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actor, err := d.Setup(ctx, "User", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	room, err := d.CreateRoom(ctx, actor.ID, "Rooms::Closed", &sql.NullString{String: "Earlier", Valid: true}, []int64{actor.ID})
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.RoomInvolvement(ctx, actor.ID, room.ID)
	if err != nil || before.Value != "mentions" {
		t.Fatal(before, err)
	}
	// No query or postcommit reconstruction may borrow a reader for this command.
	for range d.Read.Stats().MaxOpenConnections {
		conn, err := d.Read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	writer, err := d.Write.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE memberships SET involvement='invisible' WHERE room_id=? AND user_id=?", room.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE rooms SET name='Current' WHERE id=?", room.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		commit InvolvementCommit
		err    error
	}
	done := make(chan result, 1)
	waits := d.Write.Stats().WaitCount
	go func() {
		commit, err := d.ChangeInvolvement(ctx, actor.ID, room.ID, "  ")
		done <- result{commit, err}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for d.Write.Stats().WaitCount == waits {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			<-done
			t.Fatal("command did not reach writer", ctx.Err())
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.commit.Previous != "invisible" || got.commit.Value != "" || got.commit.Name != "Current" || got.commit.ID != room.ID {
		t.Fatalf("receipt used earlier state: %+v %v", got.commit, got.err)
	}
	var stored sql.NullString
	if err = d.Write.QueryRowContext(ctx, "SELECT involvement FROM memberships WHERE room_id=? AND user_id=?", room.ID, actor.ID).Scan(&stored); err != nil || stored.Valid {
		t.Fatalf("cleared involvement must retain SQL NULL: %+v %v", stored, err)
	}
}
