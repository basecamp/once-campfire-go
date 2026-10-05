package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOnlineBackupReplacesAtomically(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	u, err := d.Setup(ctx, "Backup User", "backup@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backups", "production.sqlite3")
	for i := 0; i < 2; i++ {
		if i == 1 {
			if _, err = d.Write.Exec("UPDATE users SET name='Changed' WHERE id=?", u.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err = d.Backup(ctx, destination); err != nil {
			t.Fatal(err)
		}
		snapshot, err := openConn(destination, true)
		if err != nil {
			t.Fatal(err)
		}
		var name string
		err = (&Tx{conn: snapshot}).QueryRow("SELECT name FROM users WHERE id=?", u.ID).Scan(&name)
		snapshot.Close()
		want := "Backup User"
		if i == 1 {
			want = "Changed"
		}
		if err != nil || name != want {
			t.Fatal(name, err)
		}
	}
	before, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = d.Backup(cancelled, destination); err == nil {
		t.Fatal("cancelled backup succeeded")
	}
	after, _ := os.ReadFile(destination)
	if string(before) != string(after) {
		t.Fatal("failed backup replaced good snapshot")
	}
	entries, _ := os.ReadDir(filepath.Dir(destination))
	if len(entries) != 1 {
		t.Fatal("partial backup files retained", entries)
	}
}
