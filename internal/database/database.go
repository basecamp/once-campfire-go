// Package database uses the existing Rails SQLite schema and explicit SQL.
package database

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schema string

// options is the pragma set every connection to the database file applies
// (the write DSN adds _txlock and the statement-cache cap; the read DSN adds
// mode=ro and query_only).
const options = "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=2000"

var migrations = []string{"20231215043540", "20231220143106", "20240110071740", "20240115124901", "20240130003150", "20240130213001", "20240131105830", "20240209110503", "20250825100957", "20250825100958", "20250825100959", "20251126092013", "20251126115722", "20251126130131", "20251212154340"}

// A single writer prevents pool starvation while WAL readers proceed independently.
type DB struct {
	ResetConnections    func(int64)
	PurgeBlobs          func([]int64)
	RemoveBannedContent func(int64)
	Read                *readPool
	Write               *sql.DB
	Now                 func() time.Time
	// sidebar is the in-process sidebar fragment version registry
	// (internal/database/versions.go); see DB.SidebarVersion.
	sidebar sidebarVersions
	// corpusVersion and membershipVersion are the ENGINE-30 search cache
	// version counters (see versions.go). Owned by the writers; readers only
	// load.
	corpusVersion     atomic.Int64
	membershipVersion atomic.Int64
	writer            *messageWriter
	checkpoints       *checkpointer
	// afterMu/afterN/afterCV count callers still inside the write lane's
	// after-commit work (search index insert + unread bump), which runs in
	// the request goroutine on d.Write after the shared commit. DB.Close
	// waits for the count to reach zero before closing Write so a committed
	// write never fails its after() with "database is closed".
	afterMu sync.Mutex
	afterN  int
	afterCV *sync.Cond
}

// parseWriteQueue maps a CAMPFIRE_WRITE_QUEUE value to its setting, accepting
// the same shapes as the web flags. An unrecognised value reports valid=false
// so the caller can warn while keeping the default on rather than silently
// changing behaviour.
func parseWriteQueue(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

// checkpointIntervalMS reads CAMPFIRE_CHECKPOINT_MS (milliseconds, default
// 1000): the schedule of the off-writer PASSIVE WAL checkpoint.
func checkpointIntervalMS() time.Duration {
	raw := os.Getenv("CAMPFIRE_CHECKPOINT_MS")
	if raw == "" {
		return time.Second
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		slog.Warn("invalid CAMPFIRE_CHECKPOINT_MS; using 1000", "value", raw)
		return time.Second
	}
	return time.Duration(ms) * time.Millisecond
}

func Open(path string, readers int) (*DB, error) {
	if readers < 1 {
		return nil, errors.New("database readers must be positive")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	// Reuse transaction statements on the single writer connection. The driver
	// resets bindings on reuse; results and authorization are never cached.
	w, err := sql.Open("sqlite3", uri+options+"&_txlock=immediate&_stmt_cache_size=64")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	fail := func(err error) (*DB, error) { w.Close(); return nil, err }
	if err = w.Ping(); err != nil {
		return fail(err)
	}
	if err = prepare(w); err != nil {
		return fail(err)
	}
	r, err := sql.Open("sqlite3", uri+options+"&mode=ro&_query_only=on")
	if err != nil {
		return fail(err)
	}
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	if err = r.Ping(); err != nil {
		r.Close()
		return fail(err)
	}
	db := &DB{Read: &readPool{DB: r, statements: make(map[string]*sql.Stmt)}, Write: w}
	db.afterCV = sync.NewCond(&db.afterMu)
	queue := true
	if raw, ok := os.LookupEnv("CAMPFIRE_WRITE_QUEUE"); ok {
		var valid bool
		queue, valid = parseWriteQueue(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_WRITE_QUEUE; keeping the write queue on", "value", raw)
		}
	}
	if queue {
		// The writer connection never auto-checkpoints: the separate
		// checkpointer pays the checkpoint fsyncs on its own schedule. The
		// durability contract does not move — WAL mode, synchronous=NORMAL,
		// journal_size_limit untouched (the default -1), so commits are not
		// fsynced and what committed since the last checkpoint can be lost to
		// a power failure, exactly as before.
		if _, err := w.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			r.Close()
			return fail(err)
		}
		writer := newMessageWriter(w)
		checkpoints, err := startCheckpointer(uri+options, checkpointIntervalMS())
		if err != nil {
			writer.Close()
			r.Close()
			return fail(err)
		}
		db.writer, db.checkpoints = writer, checkpoints
		slog.Info("write queue", "enabled", true, "group_commit", true)
	} else {
		slog.Info("write queue", "enabled", false)
	}
	now := time.Now
	if raw := os.Getenv("CAMPFIRE_FROZEN_TIME"); raw != "" {
		frozen, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			r.Close()
			return fail(err)
		}
		now = func() time.Time { return frozen }
	}
	db.Now = now
	return db, nil
}
func (d *DB) Close() error {
	// The writer drains queued messages first, then the final checkpoint
	// moves their frames into the database file; only then do the pools close.
	var errs []error
	if d.writer != nil {
		d.writer.Close()
		// Wait for in-flight after-commit work (search insert and unread
		// bump on the write pool) so a write whose commit landed shares the
		// close rather than failing its after() statements on the closed
		// pool, which would report a committed write as failed.
		d.afterMu.Lock()
		for d.afterN > 0 {
			d.afterCV.Wait()
		}
		d.afterMu.Unlock()
	}
	if d.checkpoints != nil {
		d.checkpoints.Close()
	}
	if err := d.Read.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := d.Write.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
func Stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000") }
func (d *DB) Transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func prepare(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("refusing to initialize a nonempty database without schema_migrations")
		}
		if _, err = tx.Exec(schema); err != nil {
			return err
		}
		for i := len(migrations) - 1; i >= 0; i-- {
			if _, err = tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)", migrations[i]); err != nil {
				return err
			}
		}
		now := Stamp(time.Now())
		for _, p := range [][2]string{{"environment", "production"}, {"schema_sha1", "f75da8dad38bfb179ffd757bd7a7c2b3f818bc29"}} {
			if _, err = tx.Exec("INSERT INTO ar_internal_metadata(key,value,created_at,updated_at) VALUES (?,?,?,?)", p[0], p[1], now, now); err != nil {
				return err
			}
		}
	}
	for _, v := range migrations {
		var found int
		if err = tx.QueryRow("SELECT count(*) FROM schema_migrations WHERE version=?", v).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return fmt.Errorf("pending migration %s: migrate with the reference app before starting", v)
		}
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS index_messages_on_room_id_and_created_at ON messages(room_id,created_at)"); err != nil {
		return err
	}
	return tx.Commit()
}

// SQLite's DATETIME(6) declaration is returned as text by go-sqlite3.
type timestamp struct{ value *time.Time }

func (t timestamp) Scan(value any) error {
	if v, ok := value.(time.Time); ok {
		*t.value = v
		return nil
	}
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("invalid timestamp type %T", value)
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			*t.value = parsed
			return nil
		}
	}
	return fmt.Errorf("invalid timestamp %q", raw)
}
