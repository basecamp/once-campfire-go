// Package database uses the existing Rails SQLite schema and explicit SQL.
package database

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schema string

var migrations = []string{"20231215043540", "20231220143106", "20240110071740", "20240115124901", "20240130003150", "20240130213001", "20240131105830", "20240209110503", "20250825100957", "20250825100958", "20250825100959", "20251126092013", "20251126115722", "20251126130131", "20251212154340"}

// A single writer prevents pool starvation while WAL readers proceed independently.
type DB struct {
	ResetConnections    func(int64)
	PurgeBlobs          func([]int64)
	RemoveBannedContent func(int64)
	Read                *readPool
	Write               *sql.DB
	Now                 func() time.Time
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
	options := "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=2000"
	w, err := sql.Open("sqlite3", uri+options+"&_txlock=immediate")
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
	now := time.Now
	if raw := os.Getenv("CAMPFIRE_FROZEN_TIME"); raw != "" {
		frozen, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			r.Close()
			return fail(err)
		}
		now = func() time.Time { return frozen }
	}
	return &DB{Read: &readPool{DB: r, statements: make(map[string]*sql.Stmt)}, Write: w, Now: now}, nil
}
func (d *DB) Close() error     { return errors.Join(d.Read.Close(), d.Write.Close()) }
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
