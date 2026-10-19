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
	"strconv"
	"sync"
	"time"

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
		stmtRestoreBusy + ";",
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
	// The WAL hook replaces the auto-checkpoint hook the pragma above turned
	// off, so it is installed after the pragmas; it reports the WAL's frame
	// count after each commit, for the writer's WAL-growth limit.
	if err := db.EnableWALHook(); err != nil {
		return fail(err)
	}
	return conn, nil
}

// WALFrames returns the WAL frame count as of this connection's most recent
// commit (Lever 4). It is not a plain load: it takes the connection's Go mutex
// (so a concurrent Close cannot free the counter under it) and makes one cgo
// call to read the C word the hook wrote. It runs once per write-lane drain,
// off the commit path and off every request's critical section; the mutex is
// uncontended there because the lane serializes transactions. The read is
// exact for this connection's own commits and never older than the last commit
// by any writer on the same WAL, because the WAL frame count is shared — but a
// foreign writer's commits are invisible until this connection commits again.
func (c *WriteConn) WALFrames() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return 0
	}
	return c.db.WALFrames()
}

// CheckpointResult is one WAL restart attempt's outcome. Attempted reports
// whether the checkpoint pragmas ran at all (false when the connection was
// unusable before the first one). Completed is false when SQLite reported the
// checkpoint incomplete in its result row — the pragma reports a refused or
// partial checkpoint in the (busy, log, checkpointed) columns rather than as
// an execution error, which is what happens while a reader still pins frames.
// PassiveNanos and RestartNanos split the attempt's wall time into the
// backfill step (page copying plus its filesystem sync) and the RESTART step
// (which includes the busy-handler wait), so the busy-wait setting can be
// judged separately from the checkpoint's own I/O.
type CheckpointResult struct {
	Attempted    bool
	Completed    bool
	WALFrames    int64
	Backfilled   int64
	PassiveNanos int64
	RestartNanos int64
}

// CheckpointBusyMS is the busy-handler timeout (milliseconds) the restart
// attempt runs with instead of the connection's normal BusyTimeoutMS. It is a
// busy-wait setting, not a wall-clock bound on the attempt: it limits only how
// long SQLite's busy handler retries a locked WAL. Connection or mutex
// acquisition, the checkpoint's page copying and filesystem synchronization,
// and scheduler delays are all outside it. The caller spaces attempts out
// (backoff) so a pinned reader cannot stall the writer on every drain.
const CheckpointBusyMS = 250

// BusyTimeoutMS is the write connections' normal busy timeout (OpenWriter
// applies it; the write DSN sets the same value). The restart attempt restores
// it after bounding its own busy wait.
const BusyTimeoutMS = 5000

// The busy-timeout statements are prebuilt: the statement cache is keyed by
// text, so formatting per attempt would allocate and miss the cache.
var (
	stmtCheckpointBusy = "PRAGMA busy_timeout = " + strconv.Itoa(CheckpointBusyMS)
	stmtRestoreBusy    = "PRAGMA busy_timeout = " + strconv.Itoa(BusyTimeoutMS)
)

// CheckpointRestart backfills the WAL with a PASSIVE checkpoint and then
// restarts it with RESTART, on this connection and between transactions (the
// caller must hold no transaction). It is the writer's WAL-growth limit: the
// writer owns the connection and holds its mutex across the whole sequence, so
// no other statement can observe the temporary busy timeout and the RESTART is
// not refused by another connection's checkpoint (the database layer also
// gates the background checkpointer against it); the next write on this
// connection resets the WAL to its first frame. Both pragmas are no-ops when
// the WAL is empty.
//
// The RESTART's result row is read, so an incomplete checkpoint (a reader
// still pinning frames, or another checkpoint holding the lock) is reported as
// Completed=false rather than as success. The attempt runs with a bounded
// busy-handler timeout (CheckpointBusyMS) and restores the normal one.
func (c *WriteConn) CheckpointRestart() (CheckpointResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return CheckpointResult{}, ErrWriterClosed
	}
	if err := c.execLocked(stmtCheckpointBusy); err != nil {
		return CheckpointResult{}, err
	}
	result, err := c.checkpointRestartLocked()
	if restoreErr := c.execLocked(stmtRestoreBusy); restoreErr != nil && err == nil {
		err = restoreErr
	}
	return result, err
}

func (c *WriteConn) checkpointRestartLocked() (CheckpointResult, error) {
	result := CheckpointResult{Attempted: true}
	passiveStart := time.Now()
	err := c.execLocked("PRAGMA wal_checkpoint(PASSIVE)")
	result.PassiveNanos = time.Since(passiveStart).Nanoseconds()
	if err != nil {
		return result, err
	}
	restartStart := time.Now()
	busy, log, checked, err := c.checkpointRowLocked("PRAGMA wal_checkpoint(RESTART)")
	result.RestartNanos = time.Since(restartStart).Nanoseconds()
	if err != nil {
		return result, err
	}
	result.Completed = busy == 0
	result.WALFrames = log
	result.Backfilled = checked
	return result, nil
}

// checkpointRowLocked runs a PRAGMA wal_checkpoint statement and reads its
// (busy, log, checkpointed) row, so the caller can tell a completed checkpoint
// from one SQLite refused. The statement is cached by text like every other.
func (c *WriteConn) checkpointRowLocked(query string) (busy, log, checked int64, err error) {
	c.recordLocked(query)
	st, err := c.acquireLocked(query)
	if err != nil {
		return 0, 0, 0, err
	}
	defer st.Reset()
	row, err := st.Step()
	if err != nil {
		return 0, 0, 0, err
	}
	if !row {
		return 0, 0, 0, errors.New("fastdb: wal_checkpoint returned no row")
	}
	if busy, err = checkpointColumnInt64(st, 0); err != nil {
		return 0, 0, 0, err
	}
	if log, err = checkpointColumnInt64(st, 1); err != nil {
		return 0, 0, 0, err
	}
	if checked, err = checkpointColumnInt64(st, 2); err != nil {
		return 0, 0, 0, err
	}
	return busy, log, checked, nil
}

// checkpointColumnInt64 reads one INTEGER column with the same strictness as
// WriteStmt.Int64: the pragma's columns are integers, so anything else is
// schema or engine drift rather than a value to coerce.
func checkpointColumnInt64(st *csqlite.Stmt, i int) (int64, error) {
	if kind := st.ColumnType(i); kind != kindInteger {
		return 0, fmt.Errorf("fastdb: column %d is %s, want INTEGER", i, kindName(kind))
	}
	return st.ColumnInt64(i), nil
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
		if rollbackErr := t.c.execLocked("ROLLBACK"); rollbackErr != nil {
			err = errors.Join(err, rollbackErr, t.c.closeLocked())
		}
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
	if err != nil {
		err = errors.Join(err, t.c.closeLocked())
	}
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

// The fused write shapes (lever 3). Each runs one cached write statement as a
// single cgo transition — the typed binds, the step, the optional
// last-insert-id capture and the reset — instead of one transition per bind
// plus one each for the step, the rowid read and the reset. The arguments are
// handed to C as plain values and bound with SQLITE_TRANSIENT, so no Go
// pointer outlives the call and nothing is retained across the reset; the SQL
// text, the bind order and the resulting rows are exactly what the unfused
// bind/step/reset sequence produced. Each method resets the statement on every
// path, so it is reusable by the next job.

// errInsertRow and errUpdateRow are the exact failures the unfused lane
// reported when a statement that must not return rows did; they stay distinct
// so callers and tests see the same text.
var (
	errInsertRow = errors.New("fastdb write: INSERT returned a row")
	errUpdateRow = errors.New("fastdb write: UPDATE returned a row")
)

// ExecInsertMessage runs the message INSERT fused: it binds
// (?=client_message_id, ?=creator_id, ?=room_id, ?=created_at, ?=updated_at)
// and returns the new row id, all in one transition.
func (s *WriteStmt) ExecInsertMessage(client string, user, room int64, stamp string) (int64, error) {
	id, row, err := s.st.ExecTyped(true,
		csqlite.Arg{Kind: csqlite.ArgText, Text: client},
		csqlite.Arg{Kind: csqlite.ArgInt, Int: user},
		csqlite.Arg{Kind: csqlite.ArgInt, Int: room},
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
	)
	if err != nil {
		return 0, err
	}
	if row {
		return 0, errInsertRow
	}
	return id, nil
}

// ExecTouchRoom runs the room-touch UPDATE fused: it binds (?=updated_at,
// ?=id).
func (s *WriteStmt) ExecTouchRoom(room int64, stamp string) error {
	_, row, err := s.st.ExecTyped(false,
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{Kind: csqlite.ArgInt, Int: room},
		csqlite.Arg{}, csqlite.Arg{}, csqlite.Arg{},
	)
	if err != nil {
		return err
	}
	if row {
		return errUpdateRow
	}
	return nil
}

// ExecInsertRichText runs the rich-text INSERT fused: it binds (?=record_id,
// ?=body, ?=created_at, ?=updated_at).
func (s *WriteStmt) ExecInsertRichText(id int64, body, stamp string) error {
	_, row, err := s.st.ExecTyped(false,
		csqlite.Arg{Kind: csqlite.ArgInt, Int: id},
		csqlite.Arg{Kind: csqlite.ArgText, Text: body},
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{},
	)
	if err != nil {
		return err
	}
	if row {
		return errInsertRow
	}
	return nil
}

// ExecInsertAttachment runs the attachment INSERT fused: it binds (?=blob_id,
// ?=record_id, ?=created_at).
func (s *WriteStmt) ExecInsertAttachment(blob, id int64, stamp string) error {
	_, row, err := s.st.ExecTyped(false,
		csqlite.Arg{Kind: csqlite.ArgInt, Int: blob},
		csqlite.Arg{Kind: csqlite.ArgInt, Int: id},
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{}, csqlite.Arg{},
	)
	if err != nil {
		return err
	}
	if row {
		return errInsertRow
	}
	return nil
}

// ExecInsertSearchIndex runs the search-index INSERT fused: it binds
// (?=rowid, ?=body).
func (s *WriteStmt) ExecInsertSearchIndex(id int64, plain string) error {
	_, row, err := s.st.ExecTyped(false,
		csqlite.Arg{Kind: csqlite.ArgInt, Int: id},
		csqlite.Arg{Kind: csqlite.ArgText, Text: plain},
		csqlite.Arg{}, csqlite.Arg{}, csqlite.Arg{},
	)
	if err != nil {
		return err
	}
	if row {
		return errInsertRow
	}
	return nil
}

// ExecBumpUnread runs the unread-bump UPDATE fused: it binds (?=unread_at,
// ?=updated_at, ?=room_id, ?=user_id, ?=connected_at cutoff).
func (s *WriteStmt) ExecBumpUnread(room, user int64, stamp, cutoff string) error {
	_, row, err := s.st.ExecTyped(false,
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{Kind: csqlite.ArgText, Text: stamp},
		csqlite.Arg{Kind: csqlite.ArgInt, Int: room},
		csqlite.Arg{Kind: csqlite.ArgInt, Int: user},
		csqlite.Arg{Kind: csqlite.ArgText, Text: cutoff},
	)
	if err != nil {
		return err
	}
	if row {
		return errUpdateRow
	}
	return nil
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
