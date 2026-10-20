package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-sqlite3"
)

// Backup writes an online SQLite snapshot beside its destination, then atomically
// replaces the previous backup only after SQLite has completed successfully.
func (d *DB) Backup(ctx context.Context, destination string) error {
	return backup(ctx, d.Read, destination)
}

// Restore replaces an offline database from its snapshot and removes obsolete
// WAL sidecars. A missing snapshot is a no-op, as required by the ONCE hook.
// The application must be stopped before restoring.
func Restore(ctx context.Context, snapshot, destination string) (result error) {
	if _, err := os.Stat(snapshot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	path, err := filepath.Abs(snapshot)
	if err != nil {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	source, err := sql.Open("sqlite3", uri+"?mode=ro&_query_only=on&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, source.Close()) }()
	// Empty or unrelated SQLite files are not Campfire snapshots. In particular,
	// never replace an installation with an empty file from a failed old backup.
	var migrations int
	if err = source.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		return fmt.Errorf("invalid Campfire snapshot: %w", err)
	}
	if migrations == 0 {
		return errors.New("snapshot has no Campfire migrations")
	}
	if err = backup(ctx, source, destination); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(destination + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func backup(ctx context.Context, reader *sql.DB, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".backup-*.sqlite3")
	if err != nil {
		return err
	}
	name := file.Name()
	file.Close()
	defer os.Remove(name)
	path, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	target, err := sql.Open("sqlite3", uri)
	if err != nil {
		return err
	}
	defer target.Close()
	source, err := reader.Conn(ctx)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer dest.Close()
	err = source.Raw(func(source any) error {
		return dest.Raw(func(dest any) (result error) {
			backup, err := dest.(*sqlite3.SQLiteConn).Backup("main", source.(*sqlite3.SQLiteConn), "main")
			if err != nil {
				return err
			}
			defer func() { result = errors.Join(result, backup.Finish()) }()
			for attempt := 0; attempt <= 50; attempt++ {
				done, err := backup.Step(-1)
				if err != nil {
					return err
				}
				if done {
					return nil
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			return errors.New("SQLite backup remained busy")
		})
	})
	if err != nil {
		return err
	}
	if err = dest.Close(); err != nil {
		return err
	}
	if err = target.Close(); err != nil {
		return err
	}
	return os.Rename(name, destination)
}
