package database

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
)

func init() {
	// database/sql serializes each connection. As in Rust, checkpoints normally
	// run separately from the writer; its 10,000-page auto-checkpoint is a backstop
	// when readers prevent the background checkpoint from completing.
	sql.Register("campfire-writer", &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		_, err := conn.Exec("PRAGMA wal_autocheckpoint=10000", nil)
		return err
	}})
}

type checkpointer struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func startCheckpoints(uri string) (*checkpointer, error) {
	db, err := sql.Open("sqlite3", uri+"?_busy_timeout=50&_journal_mode=WAL&_synchronous=NORMAL&_mutex=no")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &checkpointer{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer func() {
			if err := db.Close(); err != nil {
				slog.Error("Checkpoint connection close failed", "error", err)
			}
		}()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		last := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Inspect without copying or syncing pages. Checkpoint at the
				// normal 1,000-page cadence, or after one second of low traffic.
				var busy, pages, copied int
				if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(NOOP)").Scan(&busy, &pages, &copied); err != nil {
					if ctx.Err() == nil {
						slog.Error("WAL inspection failed", "error", err)
					}
					continue
				}
				mode := "PASSIVE"
				if pages >= 10000 {
					// Continuous writes may keep PASSIVE from catching up.
					// Restart so later commits don't repeatedly checkpoint.
					mode = "RESTART"
				} else if pages <= copied || pages-copied < 1000 && time.Since(last) < time.Second {
					continue
				}
				if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &pages, &copied); err != nil {
					if ctx.Err() == nil {
						slog.Error("WAL checkpoint failed", "error", err)
					}
				} else if busy == 0 {
					last = time.Now()
				}
			}
		}
	}()
	return c, nil
}

func (c *checkpointer) close() {
	c.once.Do(c.cancel)
	<-c.done
}
