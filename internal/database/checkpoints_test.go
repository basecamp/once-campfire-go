package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackgroundCheckpointPersistsCommittedMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.sqlite3")
	d, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// More than 1,000 pages, but below the foreground checkpoint backstop.
	// Keep every connection open: only the background worker copies these pages.
	body := strings.Repeat("a", 4<<20)
	m, err := d.CreateMessage(ctx, user.ID, rooms[0].ID, "checkpoint", body, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	for {
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Size() >= int64(len(body)) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("committed message remained only in WAL", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	stored, err := d.Message(ctx, m.ID)
	if err != nil || stored.Body != body {
		t.Fatal("checkpoint changed committed message", err)
	}
	hits, err := d.Search(ctx, user.ID, "checkpoint")
	if err != nil || len(hits) != 1 || hits[0].ID != m.ID {
		t.Fatal("checkpoint changed search index", err)
	}
}
