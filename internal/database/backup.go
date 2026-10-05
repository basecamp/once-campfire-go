package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"crawshaw.io/sqlite"
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
	target, err := sqlite.OpenConn(name, sqlite.SQLITE_OPEN_READWRITE|sqlite.SQLITE_OPEN_CREATE|sqlite.SQLITE_OPEN_NOMUTEX)
	if err != nil {
		return err
	}
	err = d.Read.With(ctx, func(source *sqlite.Conn) (result error) {
		backup, err := source.BackupInit("main", "main", target)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, backup.Finish()) }()
		for attempt := 0; attempt <= 50; attempt++ {
			err := backup.Step(-1)
			if err == nil {
				return nil
			}
			if code := sqlite.ErrCode(err); code != sqlite.SQLITE_BUSY && code != sqlite.SQLITE_LOCKED {
				return err
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
	if err = errors.Join(err, target.Close()); err != nil {
		return err
	}
	return os.Rename(name, destination)
}
