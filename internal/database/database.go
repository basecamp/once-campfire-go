// Package database uses the existing Rails SQLite schema and explicit SQL.
package database

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"crawshaw.io/sqlite"
)

//go:embed schema.sql
var schema string

var migrations = []string{"20231215043540", "20231220143106", "20240110071740", "20240115124901", "20240130003150", "20240130213001", "20240131105830", "20240209110503", "20250825100957", "20250825100958", "20250825100959", "20251126092013", "20251126115722", "20251126130131", "20251212154340"}

// DB is the application's SQLite database: one writer goroutine that owns the write connection
// and runs writes in arrival order, a checkpointer off the writer, and a pool of reader
// connections, as the reference's crates/db/src/database.rs.
type DB struct {
	ResetConnections    func(int64)
	PurgeBlobs          func([]int64)
	RemoveBannedContent func(int64)
	// PlainText converts a stored rich-text body to the text the search index keeps (the
	// reference's Env::rich_text); it runs on the writer after a message commits.
	PlainText func(body string) string
	Read      *readPool
	Write     *Writer
	Now       func() time.Time
}

// Open opens the database at path with readers reader connections (the reference's
// RAILS_MAX_THREADS), preparing the schema of an empty database.
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
	now := time.Now
	if raw := os.Getenv("CAMPFIRE_FROZEN_TIME"); raw != "" {
		frozen, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, err
		}
		now = func() time.Time { return frozen }
	}
	conn, err := openConn(path, false)
	if err != nil {
		return nil, err
	}
	if err = runWrite(conn, prepare); err != nil {
		conn.Close()
		return nil, err
	}
	// In place of the auto-checkpoint, which is itself a WAL hook (sqlite3_wal_autocheckpoint).
	if err = conn.NoteWALPages(); err != nil {
		conn.Close()
		return nil, err
	}
	checkpoints, err := startCheckpoints(path)
	if err != nil {
		conn.Close()
		return nil, err
	}
	pool := &readPool{conns: make(chan *sqlite.Conn, readers)}
	for range readers {
		reader, err := openConn(path, true)
		if err != nil {
			pool.Close()
			checkpoints.stop()
			conn.Close()
			return nil, err
		}
		pool.all = append(pool.all, reader)
		pool.conns <- reader
	}
	w := &Writer{jobs: make(chan func(*sqlite.Conn), writeQueue), done: make(chan struct{}), conn: conn, checkpoints: checkpoints}
	go w.run()
	return &DB{Read: pool, Write: w, Now: now}, nil
}

func (d *DB) Close() error     { return errors.Join(d.Write.Close(), d.Read.Close()) }
func Stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000") }

// Transaction runs fn as one BEGIN IMMEDIATE transaction on the writer goroutine, then the
// after-commit work it queued. An error from fn, or a failed commit, rolls back.
func (d *DB) Transaction(_ context.Context, fn func(*Tx) error) error {
	return d.Write.do(func(conn *sqlite.Conn) error { return runWrite(conn, fn) })
}

// runWrite is the reference's run_write: BEGIN IMMEDIATE, fn, COMMIT, then the after-commit
// queue, each hook in its own implicit transaction; the first hook error is returned after the
// rest of the queue has run.
func runWrite(conn *sqlite.Conn, fn func(*Tx) error) error {
	if _, err := exec(conn, "BEGIN IMMEDIATE", nil); err != nil {
		return err
	}
	tx := &Tx{conn: conn}
	defer func() {
		if !tx.committed && !conn.GetAutocommit() {
			exec(conn, "ROLLBACK", nil)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if _, err := exec(conn, "COMMIT", nil); err != nil {
		return err
	}
	tx.committed = true
	var first error
	for _, hook := range tx.afterCommit {
		if err := hook(tx); err != nil {
			logAfterCommit(err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func logAfterCommit(err error) { log.Printf("after_commit hook failed: %v", err) }

// writeQueue bounds queued writes; writers wait when it's full (the reference's write_queue).
const writeQueue = 256

// Writer owns the write connection on one goroutine, which runs queued writes in order.
type Writer struct {
	mu          sync.RWMutex
	closed      bool
	jobs        chan func(*sqlite.Conn)
	done        chan struct{}
	conn        *sqlite.Conn
	checkpoints *checkpoints
}

var errWriterClosed = errors.New("database writer is closed")

func (w *Writer) run() {
	defer close(w.done)
	for job := range w.jobs {
		job(w.conn)
		switch pages := w.conn.TakeWALPages(); {
		case pages == 0:
		case pages >= walLimitPages:
			w.checkpoints.restart(w.conn)
		default:
			w.checkpoints.walGrewTo(pages)
		}
	}
}

// do runs fn on the writer goroutine and waits for it. A panic in fn rolls back and panics again
// in the caller, so it fails that request rather than the writer.
func (w *Writer) do(fn func(*sqlite.Conn) error) error {
	type outcome struct {
		err      error
		panicked any
	}
	reply := make(chan outcome, 1)
	job := func(conn *sqlite.Conn) {
		defer func() {
			if p := recover(); p != nil {
				if !conn.GetAutocommit() {
					exec(conn, "ROLLBACK", nil)
				}
				reply <- outcome{panicked: p}
			}
		}()
		reply <- outcome{err: fn(conn)}
	}
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return errWriterClosed
	}
	w.jobs <- job
	w.mu.RUnlock()
	result := <-reply
	if result.panicked != nil {
		panic(result.panicked)
	}
	return result.err
}

// ExecContext runs one statement on the writer, outside a transaction.
func (w *Writer) ExecContext(_ context.Context, sql string, args ...any) (Result, error) {
	var result Result
	err := w.do(func(conn *sqlite.Conn) (err error) {
		result, err = exec(conn, sql, args)
		return err
	})
	return result, err
}

func (w *Writer) Exec(sql string, args ...any) (Result, error) {
	return w.ExecContext(context.Background(), sql, args...)
}

func (w *Writer) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.jobs)
	w.mu.Unlock()
	<-w.done
	w.checkpoints.stop()
	return w.conn.Close()
}

const (
	// SQLite's default wal_autocheckpoint, which Rails keeps: a checkpoint per 1,000 WAL pages.
	autocheckpointPages = 1000
	// The WAL size at which the writer checkpoints and restarts the WAL itself, because writes
	// never paused long enough for a background checkpoint to catch up.
	walLimitPages = 10_000
)

// checkpoints is the writer's side of the checkpointer goroutine, which runs a PASSIVE
// checkpoint on its own connection each time it's woken.
type checkpoints struct {
	wake chan struct{}
	done chan struct{}
	// Held while a checkpoint runs, so the writer's RESTART waits for the checkpointer's
	// PASSIVE rather than being refused (SQLite runs one checkpoint at a time).
	running sync.Mutex
	// The WAL's size in pages when the checkpointer was last woken.
	wokenAt int
}

func startCheckpoints(path string) (*checkpoints, error) {
	conn, err := openConn(path, false)
	if err != nil {
		return nil, err
	}
	c := &checkpoints{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer conn.Close()
		for range c.wake {
			c.running.Lock()
			checkpoint(conn, "PASSIVE")
			c.running.Unlock()
		}
	}()
	return c, nil
}

// walGrewTo wakes the checkpointer for every autocheckpointPages the WAL grows.
func (c *checkpoints) walGrewTo(pages int) {
	if pages < c.wokenAt {
		c.wokenAt = 0 // the WAL restarted
	}
	if pages-c.wokenAt < autocheckpointPages {
		return
	}
	// While a checkpoint is still due (the channel is full), the next commit tries again.
	select {
	case c.wake <- struct{}{}:
		c.wokenAt = pages
	default:
	}
}

// restart runs a RESTART checkpoint on the writer connection, between writes: it copies what the
// checkpointer hasn't and waits for readers, so that the next write restarts the WAL.
func (c *checkpoints) restart(conn *sqlite.Conn) {
	c.running.Lock()
	defer c.running.Unlock()
	checkpoint(conn, "RESTART")
}

func (c *checkpoints) stop() {
	close(c.wake)
	<-c.done
}

// checkpoint runs PRAGMA wal_checkpoint, which reports a checkpoint it couldn't finish in its
// busy column rather than as an error.
func checkpoint(conn *sqlite.Conn, mode string) {
	var busy, logPages, checkpointed int64
	rows, err := query(conn, nil, "PRAGMA wal_checkpoint("+mode+")", nil)
	if err == nil {
		row := Row{rows: rows}
		err = row.Scan(&busy, &logPages, &checkpointed)
	}
	switch {
	case err != nil:
		log.Printf("WAL checkpoint (%s) failed: %v", mode, err)
	case busy != 0:
		log.Printf("WAL checkpoint (%s) couldn't finish", mode)
	}
}

// busyTimeout and the pragmas are the reference's schema::configure_connection.
const busyTimeout = 5000 * time.Millisecond

var connectionPragmas = []string{
	"PRAGMA foreign_keys=ON",
	"PRAGMA journal_mode=wal",
	"PRAGMA synchronous=normal",
	"PRAGMA journal_size_limit=67108864",
	"PRAGMA cache_size=2000",
}

func openConn(path string, reader bool) (*sqlite.Conn, error) {
	conn, err := sqlite.OpenConn(path, sqlite.SQLITE_OPEN_READWRITE|sqlite.SQLITE_OPEN_CREATE|sqlite.SQLITE_OPEN_URI|sqlite.SQLITE_OPEN_NOMUTEX)
	if err != nil {
		return nil, err
	}
	conn.SetBusyTimeout(busyTimeout)
	pragmas := connectionPragmas
	if reader {
		pragmas = append(pragmas[:len(pragmas):len(pragmas)], "PRAGMA query_only=ON")
	}
	for _, pragma := range pragmas {
		if _, err = exec(conn, pragma, nil); err != nil {
			conn.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return conn, nil
}

func prepare(tx *Tx) error {
	var exists int
	err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&exists)
	if err != nil {
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
	_, err = tx.Exec("CREATE INDEX IF NOT EXISTS index_messages_on_room_id_and_created_at ON messages(room_id,created_at)")
	return err
}

// timestamp scans a DATETIME(6) column, which SQLite stores as text.
type timestamp struct{ value *time.Time }

// parse reads Rails' "YYYY-MM-DD HH:MM:SS[.ffffff]" (UTC) by position, as the reference's
// Timestamp does, and falls back to time.Parse for other layouts.
func (t timestamp) parse(raw string) error {
	if v, ok := parseStamp(raw); ok {
		*t.value = v
		return nil
	}
	return t.Scan(raw)
}

func parseStamp(s string) (time.Time, bool) {
	if len(s) < 19 || s[4] != '-' || s[7] != '-' || s[10] != ' ' || s[13] != ':' || s[16] != ':' {
		return time.Time{}, false
	}
	year, ok1 := digits(s[0:4])
	month, ok2 := digits(s[5:7])
	day, ok3 := digits(s[8:10])
	hour, ok4 := digits(s[11:13])
	minute, ok5 := digits(s[14:16])
	second, ok6 := digits(s[17:19])
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) || month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	nanos := 0
	if rest := s[19:]; rest != "" {
		if rest[0] != '.' || len(rest) < 2 || len(rest) > 10 {
			return time.Time{}, false
		}
		fraction, ok := digits(rest[1:])
		if !ok {
			return time.Time{}, false
		}
		for range 10 - len(rest) {
			fraction *= 10
		}
		nanos = fraction
	}
	v := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.UTC)
	if v.Day() != day {
		return time.Time{}, false // February 30th and the like: let time.Parse reject it
	}
	return v, true
}

func digits(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

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
