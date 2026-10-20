package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestSubmittedSearchBodyResolvesWriterCurrentMentions(t *testing.T) {
	d := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, err := d.Setup(ctx, "Earlier", "user@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets(strings.Repeat("a", 128))
	if err != nil {
		t.Fatal(err)
	}
	token := secrets.SGID(fmt.Sprintf("gid://campfire/User/%d", user.ID), "attachable", time.Time{})
	body := `<action-text-attachment sgid="` + token + `"></action-text-attachment>`

	// Preparation must not resolve names through a reader before the command's
	// transaction. Hold all readers and queue the command behind a profile edit.
	for range d.Read.Stats().MaxOpenConnections {
		conn, err := d.Read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := d.Write.Stats().WaitCount
	type result struct {
		commit MessageCommit
		err    error
	}
	done := make(chan result, 1)
	go func() {
		commit, err := d.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("", body))
		done <- result{commit, err}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for d.Write.Stats().WaitCount == before {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("command did not reach the writer", ctx.Err())
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET name='Current' WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE rooms SET name='Current room' WHERE id=?", rooms[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.commit.Creator != "Current" || got.commit.Room.Name != "Current room" {
		t.Fatalf("receipt did not capture writer-current author and room: %+v", got.commit)
	}
	var plain string
	if err := d.Write.QueryRowContext(ctx, "SELECT body FROM message_search_index WHERE rowid=?", got.commit.ID).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != "@Current" {
		t.Fatalf("committed search text = %q, want writer-current mention", plain)
	}
}
