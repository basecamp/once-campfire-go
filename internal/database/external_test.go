package database

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func openExternal(t *testing.T, d *DB) *sql.DB {
	t.Helper()
	other, err := sql.Open("sqlite3", d.FilePath()+"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	return other
}

func insertExternalMessage(t *testing.T, other *sql.DB, user, room int64, client, plain string) int64 {
	t.Helper()
	stamp := Stamp(time.Now())
	result, err := other.Exec(`INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES (?,?,?,?,?)`, client, user, room, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	body := "<p>" + plain + "</p>"
	if _, err = other.Exec(`INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)`, id, body, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(`INSERT INTO message_search_index(rowid,body) VALUES (?,?)`, id, plain); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestExternalCommitRefreshesWindowAccountAndMembership(t *testing.T) {
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
	if _, err = d.CreateMessage(ctx, user.ID, room, "seed", "<p>seed</p>", "seed"); err != nil {
		t.Fatal(err)
	}
	secret, err := d.CreateRoom(ctx, user.ID, "Rooms::Closed", "Secret Plans", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.MessagePageReferences(ctx, room, 0, "around"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.MessagePageReferences(ctx, room, 0, "around"); err != nil {
		t.Fatal(err)
	}
	hits, misses := d.PageCacheStats()
	if hits != 1 || misses != 1 {
		t.Fatalf("window was not warm: hits=%d misses=%d", hits, misses)
	}
	if _, err = d.Account(ctx); err != nil {
		t.Fatal(err)
	}
	before := d.ContentGeneration()
	other := openExternal(t, d)
	if _, err = other.Exec(`UPDATE accounts SET name=?`, "Renamed Elsewhere"); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(`DELETE FROM memberships WHERE user_id=? AND room_id=?`, user.ID, secret.ID); err != nil {
		t.Fatal(err)
	}
	if d.ContentGeneration() == before {
		t.Fatal("external membership change did not move the content generation")
	}
	account, err := d.Account(ctx)
	if err != nil || account.Name != "Renamed Elsewhere" {
		t.Fatalf("account cache kept the old name: %+v %v", account, err)
	}
	visible, err := d.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range visible {
		if candidate.ID == secret.ID {
			t.Fatal("revoked room still visible")
		}
	}

	// Warm again, then a foreign insert followed by our own write. The local
	// write must not publish a window that skipped the foreign message.
	if _, err = d.MessagePageReferences(ctx, room, 0, "around"); err != nil {
		t.Fatal(err)
	}
	externalID := insertExternalMessage(t, other, user.ID, room, "external-1", "external-ping")
	created, err := d.CreateMessage(ctx, user.ID, room, "local", "<p>local</p>", "local")
	if err != nil {
		t.Fatal(err)
	}
	window, err := d.MessagePageReferences(ctx, room, 0, "around")
	if err != nil {
		t.Fatal(err)
	}
	var sawExternal, sawLocal bool
	for _, message := range window {
		if message.ID == externalID {
			sawExternal = true
		}
		if message.ID == created.ID {
			sawLocal = true
		}
	}
	if !sawExternal || !sawLocal {
		t.Fatalf("window missed an external or local message: %+v", window)
	}
}
