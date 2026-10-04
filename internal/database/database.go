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
	"sync"
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
	checkpoints         *checkpointer
	writes              chan writeJob
	writer              sync.RWMutex // held for writing once closed
	closed              bool
	writerDone          chan struct{}
}

// Transactions run in order on one writer goroutine, as on the reference's writer
// thread. Handing each request's goroutine the connection in turn left it idle
// until that goroutine was scheduled, and every busy read made the wait longer.
const writeQueue = 256

var ErrClosed = errors.New("database closed")

type writeJob struct {
	ctx  context.Context
	fn   func(*sql.Tx) error
	done chan writeResult
}
type writeResult struct {
	err      error
	panicked any
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
	if err = registerDrivers(); err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	// Each connection is used by one goroutine at a time, so SQLite's own per-call
	// connection mutex is unnecessary, as in the reference (SQLITE_OPEN_NO_MUTEX).
	options := "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=2000&_mutex=no"
	// Reuse transaction statements on the single writer connection. The driver
	// resets bindings on reuse; results and authorization are never cached.
	w, err := sql.Open(writerDriver, uri+options+"&_txlock=immediate&_stmt_cache_size=64")
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
	// The reference's limit, which truncates the WAL when the writer restarts it.
	if _, err = w.Exec("PRAGMA journal_size_limit=67108864"); err != nil {
		return fail(err)
	}
	// The hook finds the checkpointer by the file name SQLite reports.
	var seq int
	var schemaName, file string
	if err = w.QueryRow("PRAGMA database_list").Scan(&seq, &schemaName, &file); err != nil {
		return fail(err)
	}
	c, err := sql.Open(readerDriver, uri+options)
	if err != nil {
		return fail(err)
	}
	checkpoints := startCheckpointer(file, c)
	fail = func(err error) (*DB, error) { w.Close(); checkpoints.close(); return nil, err }
	r, err := sql.Open(readerDriver, uri+options+"&mode=ro&_query_only=on")
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
	d := &DB{Read: &readPool{DB: r, statements: make(map[string]*sql.Stmt)}, Write: w, Now: now, checkpoints: checkpoints, writes: make(chan writeJob, writeQueue), writerDone: make(chan struct{})}
	go d.runWrites()
	return d, nil
}

// Queued transactions finish first; the checkpointer stops after the writer, whose
// commits wake it.
func (d *DB) Close() error {
	d.writer.Lock()
	if !d.closed {
		d.closed = true
		close(d.writes)
	}
	d.writer.Unlock()
	<-d.writerDone
	err := errors.Join(d.Read.Close(), d.Write.Close())
	return errors.Join(err, d.checkpoints.close())
}
func Stamp(t time.Time) string { return string(AppendStamp(nil, t)) }

// AppendStamp appends t as Stamp formats it, without parsing the layout each time.
func AppendStamp(b []byte, t time.Time) []byte {
	t = t.UTC()
	year, month, day := t.Date()
	if year < 0 || year > 9999 {
		return t.AppendFormat(b, "2006-01-02 15:04:05.000000")
	}
	hour, minute, second := t.Clock()
	digits := func(b []byte, value, width int) []byte {
		for divisor := pow10[width-1]; divisor > 0; divisor /= 10 {
			b = append(b, byte('0'+value/divisor%10))
		}
		return b
	}
	b = digits(b, year, 4)
	b = append(b, '-')
	b = digits(b, int(month), 2)
	b = append(b, '-')
	b = digits(b, day, 2)
	b = append(b, ' ')
	b = digits(b, hour, 2)
	b = append(b, ':')
	b = digits(b, minute, 2)
	b = append(b, ':')
	b = digits(b, second, 2)
	b = append(b, '.')
	return digits(b, t.Nanosecond()/1000, 6)
}

var pow10 = [...]int{1, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000}

// parseStamp reads the "2006-01-02 15:04:05[.ffffff]" text Rails and Stamp store,
// as time.Parse would; other forms are left to time.Parse.
func parseStamp(raw string) (time.Time, bool) {
	if len(raw) != 19 && (len(raw) < 21 || len(raw) > 29 || raw[19] != '.') {
		return time.Time{}, false
	}
	number := func(from, to int) (int, bool) {
		n := 0
		for i := from; i < to; i++ {
			c := raw[i]
			if c < '0' || c > '9' {
				return 0, false
			}
			n = n*10 + int(c-'0')
		}
		return n, true
	}
	if raw[4] != '-' || raw[7] != '-' || raw[10] != ' ' || raw[13] != ':' || raw[16] != ':' {
		return time.Time{}, false
	}
	year, ok1 := number(0, 4)
	month, ok2 := number(5, 7)
	day, ok3 := number(8, 10)
	hour, ok4 := number(11, 13)
	minute, ok5 := number(14, 16)
	second, ok6 := number(17, 19)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) || month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	if day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return time.Time{}, false
	}
	nanos := 0
	if len(raw) > 19 {
		fraction, ok := number(20, len(raw))
		if !ok {
			return time.Time{}, false
		}
		nanos = fraction * pow10[9-(len(raw)-20)]
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.UTC), true
}

// Transaction runs fn in an immediate transaction on the writer goroutine, after the
// transactions queued before it. A panic in fn is raised again in the caller.
func (d *DB) Transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	job := writeJob{ctx: ctx, fn: fn, done: make(chan writeResult, 1)}
	d.writer.RLock()
	if d.closed {
		d.writer.RUnlock()
		return ErrClosed
	}
	select {
	case d.writes <- job:
	case <-ctx.Done():
		d.writer.RUnlock()
		return ctx.Err()
	}
	d.writer.RUnlock()
	result := <-job.done
	if result.panicked != nil {
		panic(result.panicked)
	}
	return result.err
}
func (d *DB) runWrites() {
	defer close(d.writerDone)
	for job := range d.writes {
		job.done <- d.run(job)
	}
}
func (d *DB) run(job writeJob) (result writeResult) {
	defer func() {
		if p := recover(); p != nil {
			result.panicked = p
		}
	}()
	result.err = d.transaction(job.ctx, job.fn)
	return result
}
func (d *DB) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.Write.BeginTx(uncancelled(ctx), nil)
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
	if parsed, ok := parseStamp(raw); ok {
		*t.value = parsed
		return nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			*t.value = parsed
			return nil
		}
	}
	return fmt.Errorf("invalid timestamp %q", raw)
}
