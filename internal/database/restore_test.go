package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreSnapshotAndSidecars(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Setup(ctx, "Snapshot owner", "restore@test", "digest"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.sqlite3")
	if err := db.Backup(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored", "custom.sqlite3")
	if err = Restore(ctx, snapshot, destination); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.WriteFile(destination+suffix, []byte("obsolete sidecar"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = Restore(ctx, snapshot, destination); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err = os.Stat(destination + suffix); !os.IsNotExist(err) {
			t.Fatalf("obsolete %s retained: %v", suffix, err)
		}
	}
	restored, err := Open(destination, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var name, integrity string
	if err = restored.Read.QueryRow("SELECT name FROM users").Scan(&name); err != nil || name != "Snapshot owner" {
		t.Fatal(name, err)
	}
	if err = restored.Read.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	after, err := os.ReadFile(snapshot)
	if err != nil || string(before) != string(after) {
		t.Fatal("restore modified its source snapshot", err)
	}
}

func TestRestoreFailurePreservesDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "destination.sqlite3")
	for _, item := range []struct {
		name    string
		present bool
		cancel  bool
		wantErr bool
	}{
		{"absent", false, false, false},
		{"invalid", true, false, true},
		{"empty", true, false, true},
		{"cancelled", true, true, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			snapshot := filepath.Join(root, item.name+".sqlite3")
			if item.cancel {
				db := testDB(t)
				if err := db.Backup(context.Background(), snapshot); err != nil {
					t.Fatal(err)
				}
			} else if item.present {
				data := []byte("not a database")
				if item.name == "empty" {
					data = nil
				}
				if err := os.WriteFile(snapshot, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.WriteFile(destination+suffix, []byte("retain"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if item.cancel {
				cancel()
			}
			if err := Restore(ctx, snapshot, destination); (err != nil) != item.wantErr {
				t.Fatal(err)
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				data, err := os.ReadFile(destination + suffix)
				if err != nil || string(data) != "retain" {
					t.Fatal("failed/absent restore changed destination", suffix, err)
				}
			}
			partials, err := filepath.Glob(filepath.Join(root, ".backup-*"))
			if err != nil || len(partials) != 0 {
				t.Fatal("failed restore retained staging files", partials, err)
			}
		})
	}
}
