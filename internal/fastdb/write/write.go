// Package write is fastdb's direct write side (ENGINE-55): a thin serialized
// read-write connection able to run the message create transaction, prepared
// statements cached by text and reused across jobs, positional binds
// (int64/text/null), and step/reset lifecycle. It talks to SQLite through
// internal/fastdb/csqlite exactly like the read side; it deliberately does
// not import the fastdb read package, so the read package's differential
// tests can keep importing internal/database (which imports this package)
// without a cycle. The write lane in internal/database drives it instead of
// database/sql when CAMPFIRE_FASTDB_WRITE is on (the default); the
// database/sql writers stay the reference for every other write (sessions,
// accounts, updates), exactly as the read layer keeps the database/sql
// readers as reference.
//
// # Concurrency model
//
// A WriteConn is one SQLite connection opened with SQLITE_OPEN_NOMUTEX and
// serialized in Go: Begin takes the connection's mutex, and the returned
// WriteTx owns the connection until Commit or Rollback releases it. Only one
// transaction may be open at a time, and a transaction's statement methods
// run on the goroutine that began it — the same single-owner contract the
// database/sql lane gets from SetMaxOpenConns(1).
//
// # CGO safety
//
// The same boundary as the read side: SQL text is copied to C memory for the
// prepare call (SQLite keeps its own copy), bound text is handed over with
// SQLITE_TRANSIENT (copied during the call), and no Go pointer is stored in
// C. See package csqlite.
//
// # Error mapping
//
// Statement failures surface *csqlite.Error errors whose Unwrap names the
// primary result-code sentinels (ErrConstraint, ErrBusy, …). Single-row
// lookups that find no row return ErrNoRows, which aliases database/sql's
// sentinel, so callers can move between the lanes without changing error
// checks.
package write

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

// ErrNoRows aliases database/sql's sentinel, matching fastdb.ErrNoRows and
// the legacy readers.
var ErrNoRows = sql.ErrNoRows

// Column value classes; the constants match sqlite3_column_type and the
// read side's Kind values.
const (
	kindInteger = int(csqlite.TypeInteger)
	kindFloat   = int(csqlite.TypeFloat)
	kindText    = int(csqlite.TypeText)
	kindBlob    = int(csqlite.TypeBlob)
	kindNull    = int(csqlite.TypeNull)
)

// kindName names a column value class for error messages.
func kindName(kind int) string {
	switch kind {
	case kindInteger:
		return "INTEGER"
	case kindFloat:
		return "FLOAT"
	case kindText:
		return "TEXT"
	case kindBlob:
		return "BLOB"
	case kindNull:
		return "NULL"
	default:
		return "UNKNOWN"
	}
}

// WriteConn is one serialized read-write connection: a statement cache plus
// the mutex that hands the connection to one transaction at a time.
type WriteConn struct {
	db     *csqlite.Conn
	mu     sync.Mutex
	stmts  map[string]*csqlite.Stmt
	limit  int
	closed bool
	// record, when set (tests), receives every statement text this
	// connection executes — transaction control and cached statements alike.
	// It is invoked with the connection's mutex held, so a recorded stream
	// is exactly the statement order that ran on the connection.
	record func(query string)
}

// SetRecorder installs the statement recorder, or clears it with nil. It is
// a test hook: the database lane wires its statement-stream test through it
// (the numbering contract the counting driver pins for the database/sql
// lane).
func (c *WriteConn) SetRecorder(rec func(query string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record = rec
}

func (c *WriteConn) recordLocked(query string) {
	if c.record != nil {
		c.record(query)
	}
}

// OpenWriter opens path read-write and applies the pragma set the
// internal/database write DSN applies (busy_timeout=5000, foreign_keys=on,
// journal_mode=WAL, synchronous=NORMAL, cache_size=2000) plus
// wal_autocheckpoint=0: the writer connection never pays the checkpoint's
// WAL and database fsyncs, the separately scheduled checkpointer does.
// maxStatements caps the prepared-statement cache; values below one mean the
// default of 32 (the create transaction and its transaction control need far
// fewer).
func OpenWriter(path string, maxStatements int) (*WriteConn, error) {
	db, err := csqlite.OpenReadWrite(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*WriteConn, error) {
		db.Close()
		return nil, err
	}
	if maxStatements <= 0 {
		maxStatements = 32
	}
	conn := &WriteConn{db: db, limit: maxStatements, stmts: make(map[string]*csqlite.Stmt, maxStatements)}
	for _, pragma := range [...]string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = 1;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA cache_size = 2000;",
		"PRAGMA wal_autocheckpoint = 0;",
	} {
		if err := db.Exec(pragma); err != nil {
			return fail(err)
		}
	}
	return conn, nil
}

// Close finalizes the cached statements and closes the connection. It is
// idempotent; no transaction may be open, and using the connection afterwards
// fails Begin with ErrWriterClosed.
func (c *WriteConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

// ErrWriterClosed is reported by Begin after Close; it mirrors the database
// lane's ErrWriterClosed contract in shape (the lane in internal/database
// keeps its own sentinel for the queue).
var ErrWriterClosed = errors.New("fastdb: writer connection closed")

func (c *WriteConn) closeLocked() error {
	if c.db == nil {
		return nil
	}
	var first error
	for query, st := range c.stmts {
		st.Reset()
		if err := st.Finalize(); err != nil && first == nil {
			first = err
		}
		delete(c.stmts, query)
	}
	if err := c.db.Close(); err != nil && first == nil {
		first = err
	}
	c.db = nil
	c.closed = true
	return first
}

// Begin starts a transaction with BEGIN IMMEDIATE (the same statement the
// database/sql lane gets from _txlock=immediate) and takes the connection's
// mutex: a transaction owns the connection until Commit or Rollback. A
// context that is already cancelled fails the begin before anything runs,
// like the database/sql lane's BeginTx.
func (c *WriteConn) Begin(ctx context.Context) (*WriteTx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if err := c.execLocked("BEGIN IMMEDIATE"); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	return &WriteTx{c: c}, nil
}

// execLocked prepares (cached) and steps one statement to completion. The
// caller holds the connection's mutex.
func (c *WriteConn) execLocked(query string) error {
	c.recordLocked(query)
	st, err := c.acquireLocked(query)
	if err != nil {
		return err
	}
	defer st.Reset()
	for {
		row, err := st.Step()
		if err != nil {
			return err
		}
		if !row {
			return nil
		}
	}
}

// acquireLocked returns the cached statement for query, preparing and caching
// it while the cap allows; past the cap it prepares per use. A prepare
// failure is returned without caching, so a statement whose table disappears
// (a dropped FTS index, in the after-commit failure tests) re-prepares and
// fails on every use, exactly like the database/sql lane's per-execute
// prepares. A closed connection reports ErrWriterClosed — the guard keeps a
// statement from dereferencing a closed sqlite handle and, because every
// caller releases the mutex on the error path, keeps a panic from ever
// leaving the connection locked.
func (c *WriteConn) acquireLocked(query string) (*csqlite.Stmt, error) {
	if c.db == nil {
		return nil, ErrWriterClosed
	}
	if st, ok := c.stmts[query]; ok {
		return st, nil
	}
	st, err := c.db.Prepare(query)
	if err != nil {
		return nil, err
	}
	if len(c.stmts) < c.limit {
		c.stmts[query] = st
	}
	return st, nil
}

// WriteTx is one transaction on a WriteConn. Its methods must run on the
// goroutine that called Begin, between Begin and Commit/Rollback; the
// connection is owned by the transaction for that whole span. Commit or
// Rollback finalizes the transaction exactly once — a later call reports
// sql.ErrTxDone, the same sentinel the database/sql lane's tx reports on a
// double-finalize — and releases the connection.
type WriteTx struct {
	c    *WriteConn
	done bool
}

// Commit ends the transaction and releases the connection. The returned
// error names the commit failure; the transaction is rolled back so the
// connection ends clean, exactly like the database/sql lane (whose failed
// commit also finalizes the transaction), and the connection is released
// either way.
func (t *WriteTx) Commit() error {
	if t.done {
		return sql.ErrTxDone
	}
	err := t.c.execLocked("COMMIT")
	if err != nil {
		t.done = true
		t.c.execLocked("ROLLBACK")
		t.c.mu.Unlock()
		return err
	}
	t.done = true
	t.c.mu.Unlock()
	return nil
}

// Rollback ends the transaction without committing and releases the
// connection. Rollback on a failed statement may itself report the original
// failure; callers that already handled it drop the error.
func (t *WriteTx) Rollback() error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	err := t.c.execLocked("ROLLBACK")
	t.c.mu.Unlock()
	return err
}

// Stmt returns the cached prepared statement for query, owned by the
// connection. Bind, step, read and reset it before running any other
// statement of the same transaction. Acquisition records the query with the
// connection's recorder, under the connection's mutex, so the recorded
// stream matches the execution order exactly.
func (t *WriteTx) Stmt(query string) (*WriteStmt, error) {
	t.c.recordLocked(query)
	st, err := t.c.acquireLocked(query)
	if err != nil {
		return nil, err
	}
	return &WriteStmt{c: t.c, st: st}, nil
}

// Exec runs one statement for effect (transaction control, pragmas), prepared
// and cached by text. It is the raw-exec surface of the lane; the create
// statements never take it (they bind positionally through Stmt).
func (t *WriteTx) Exec(query string) error {
	return t.c.execLocked(query)
}

// LastInsertID returns the rowid of the most recent successful INSERT on the
// connection, to be read immediately after that insert's Step.
func (t *WriteTx) LastInsertID() int64 { return t.c.db.LastInsertRowID() }

// Changes returns the number of rows modified by the most recently completed
// statement on the connection.
func (t *WriteTx) Changes() int64 { return t.c.db.Changes() }

// WriteStmt is one prepared statement execution on a write transaction. It
// wraps the cached csqlite statement; Reset restores it for the next job.
// Columns are read with the same strictness as Rows: Int64 requires an
// INTEGER column, Text copies and errors on NULL.
type WriteStmt struct {
	c  *WriteConn
	st *csqlite.Stmt
}

// BindInt64 binds parameter i (1-based) as an integer.
func (s *WriteStmt) BindInt64(i int, v int64) error { return s.st.BindInt64(i, v) }

// BindText binds parameter i (1-based) as text; SQLite copies the bytes
// during the call.
func (s *WriteStmt) BindText(i int, v string) error { return s.st.BindText(i, v) }

// BindTextBytes binds parameter i (1-based) from a byte slice, with the same
// copy-on-bind rule as BindText.
func (s *WriteStmt) BindTextBytes(i int, v []byte) error { return s.st.BindTextBytes(i, v) }

// BindNull binds parameter i (1-based) as SQL NULL.
func (s *WriteStmt) BindNull(i int) error { return s.st.BindNull(i) }

// Step advances one row: it reports true for a row, false at SQLITE_DONE and
// a typed error for any other failure.
func (s *WriteStmt) Step() (bool, error) { return s.st.Step() }

// Reset ends the current execution, leaving the statement reusable. Errors
// from the most recent step are intentionally dropped: the caller already
// saw them.
func (s *WriteStmt) Reset() { s.st.Reset() }

// Int64 returns column i as an integer and requires an INTEGER column, with
// the same strictness as Rows.Int64: any TEXT/REAL/NULL surviving on the
// columns this layer reads is an error rather than a coerced value.
func (s *WriteStmt) Int64(i int) (int64, error) {
	// Mirrors the read side's Rows.Int64 strictness (fastdb/rows.go): an
	// INTEGER column is required; any surviving TEXT/REAL/NULL is a
	// schema-drift or corruption signal, so it is an error rather than a
	// coerced value.
	if kind := s.st.ColumnType(i); kind != kindInteger {
		return 0, fmt.Errorf("fastdb: column %d is %s, want INTEGER", i, kindName(int(kind)))
	}
	return s.st.ColumnInt64(i), nil
}

// Text returns column i as a freshly allocated string. NULL is ErrNull; an
// empty string is not.
func (s *WriteStmt) Text(i int) (string, error) { return s.st.ColumnText(i) }

// IsNull reports whether column i is SQL NULL.
func (s *WriteStmt) IsNull(i int) bool { return s.st.ColumnType(i) == csqlite.TypeNull }
