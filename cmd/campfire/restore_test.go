package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestBackupRestoreUsesConfiguredPaths(t *testing.T) {
	for _, item := range []struct {
		name, storage, path, environment string
	}{
		{"default", "", "", ""},
		{"storage and environment", "alternate storage", "", "staging"},
		{"database", "", "external database/custom.sqlite3", ""},
		{"combined", "alternate storage", "external database/custom.sqlite3", "staging"},
		{"URI metacharacters", "alternate?storage", "external?database/custom#file.sqlite3", "staging"},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("CAMPFIRE_STORAGE_PATH", item.storage)
			t.Setenv("CAMPFIRE_DATABASE_PATH", item.path)
			t.Setenv("RAILS_ENV", item.environment)
			t.Setenv("SECRET_KEY_BASE", "restore-test")
			args := os.Args
			t.Cleanup(func() { os.Args = args })
			config := databaseConfigFromEnv()
			db, err := database.Open(config.Path, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Setup(context.Background(), "Before backup", "restore@test", "digest"); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			os.Args = []string{"campfire", "backup"}
			if err = run(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(config.Path, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Write.Exec("UPDATE users SET name='After backup'"); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			// Offline restore, unlike opening the application, needs no cookie secret.
			t.Setenv("SECRET_KEY_BASE", "")
			os.Args = []string{"campfire", "restore"}
			if err = run(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(config.Path, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var name string
			if err = db.Read.QueryRow("SELECT name FROM users").Scan(&name); err != nil || name != "Before backup" {
				t.Fatalf("configured restore failed: %q %v", name, err)
			}
			if _, err = os.Stat(filepath.Join(config.Storage, "backups", filepath.Base(config.Path))); err != nil {
				t.Fatal(err)
			}
		})
	}
}
