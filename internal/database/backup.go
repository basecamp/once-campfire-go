package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-sqlite3"
)

// Backup writes an online SQLite snapshot beside its destination, then atomically
// replaces the previous backup only after SQLite has completed successfully.
func (d *DB) Backup(ctx context.Context, destination string) error {
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
	target, err := sql.Open("sqlite3", name)
	if err != nil {
		return err
	}
	defer target.Close()
	source, err := d.Read.Conn(ctx)
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
