package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSchemaAndMessageTransaction(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	source, err := os.ReadFile("../../reference/crates/db/src/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != schema {
		t.Fatal("schema diverged from pinned reference")
	}
	u, err := d.Setup(ctx, "David", "david@example.test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, u.ID)
	if err != nil || len(rooms) != 1 {
		t.Fatalf("rooms: %v %v", rooms, err)
	}
	if _, err = d.Setup(ctx, "Other", "other@example.test", "digest"); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("repeated setup: %v", err)
	}
	m, err := d.CreateMessage(ctx, u.ID, rooms[0].ID, "", "<p>running dogs</p>", "running dogs")
	if err != nil {
		t.Fatal(err)
	}
	id, err := uuid.Parse(m.ClientID)
	if err != nil || m.ClientID != id.String() || id[6]>>4 != 4 || id[8]&0xc0 != 0x80 {
		t.Fatalf("generated client ID must remain a canonical UUID v4: %q (%v)", m.ClientID, err)
	}
	messages, err := d.Messages(ctx, rooms[0].ID, 0)
	if err != nil || len(messages) != 1 || messages[0].ID != m.ID ||
		messages[0].Body != "<p>running dogs</p>" {
		t.Fatalf("messages: %v %v", messages, err)
	}
	hits, err := d.SearchReferences(ctx, u.ID, "run")
	if err != nil || len(hits) != 1 {
		t.Fatalf("porter search: %v %v", hits, err)
	}
	if _, err = d.CreateMessage(ctx, u.ID, 12345, "", "hidden", "hidden"); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("unauthorized write: %v", err)
	}
	if hits, err = d.SearchReferences(ctx, u.ID+1, "run"); err != nil || len(hits) != 0 {
		t.Fatalf("private search leaked: %v %v", hits, err)
	}
	// An FTS failure must roll back the message and its rich text together.
	if _, err = d.Write.Exec("DROP TABLE message_search_index"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.CreateMessage(ctx, u.ID, rooms[0].ID, "", "rollback", "rollback"); err == nil {
		t.Fatal("expected failed index write")
	}
	var count int
	if err = d.Read.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("partial write: %d %v", count, err)
	}
}

func TestSessionRevocation(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	u, err := d.Setup(ctx, "User", "u@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	token, err := d.StartSession(ctx, u.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.SessionUser(ctx, token)
	if err != nil || got.ID != u.ID {
		t.Fatal(got, err)
	}
	if _, err = d.Write.Exec("UPDATE users SET status=2 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.SessionUser(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("banned user session accepted: %v", err)
	}
}

func TestPendingMigrationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	d, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Write.Exec("DELETE FROM schema_migrations WHERE version=?", migrations[0]); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if d, err = Open(path, 1); err == nil {
		d.Close()
		t.Fatal("missing migration accepted")
	} else if !strings.Contains(err.Error(), "pending migration") {
		t.Fatal(err)
	}
}
