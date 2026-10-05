// Package csqlite is a minimal direct-SQLite binding for fastdb: exactly the
// calls the read layer uses, nothing more.
//
// It is not a database/sql driver. The SQLite symbols come from the
// amalgamation compiled into the binary by github.com/mattn/go-sqlite3, which
// the application links through internal/database; csqlite does not import the
// driver itself. A binary that links fastdb must also link go-sqlite3 (the
// repository entry points do). The vendored sqlite3-binding.h must match the
// module version — see README.md in this directory.
//
// # CGO safety
//
// No C pointer ever refers to Go memory after a call returns:
//
//   - Paths, SQL text and pragma text are copied into C memory for the call
//     and freed when it returns (sqlite3_prepare_v2 copies the SQL).
//   - BindText and BindTextBytes pass Go string/byte data to
//     sqlite3_bind_text with SQLITE_TRANSIENT, which makes SQLite copy the
//     bytes before the call returns; runtime.KeepAlive pins the argument
//     across the call. SQLITE_STATIC is never used.
//   - ColumnBytes returns a view of SQLite-owned memory and is documented as
//     valid only until the next Step/Reset/Finalize. ColumnText and the
//     fastdb ColumnTextInto copy before returning.
package csqlite

/*
#include <stdlib.h>
#include "sqlite3-binding.h"

// fastdb_bind_text binds with SQLITE_TRANSIENT: SQLite copies the bytes during
// the call, so Go memory is never retained. A NULL pointer with n == 0 binds
// an empty string (sqlite3_bind_text would otherwise bind SQL NULL).
static int fastdb_bind_text(sqlite3_stmt *stmt, int idx, const void *val, int n) {
	if (val == NULL) val = "";
	return sqlite3_bind_text(stmt, idx, (const char *)val, n, SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

// Primary SQLite result codes the layer names. SQLITE_BUSY, SQLITE_NOTADB and
// friends arrive through *Error and the sentinels below.
const (
	ResultOK         = int(C.SQLITE_OK)
	ResultError      = int(C.SQLITE_ERROR)
	ResultBusy       = int(C.SQLITE_BUSY)
	ResultLocked     = int(C.SQLITE_LOCKED)
	ResultReadOnly   = int(C.SQLITE_READONLY)
	ResultCorrupt    = int(C.SQLITE_CORRUPT)
	ResultCantOpen   = int(C.SQLITE_CANTOPEN)
	ResultNotADB     = int(C.SQLITE_NOTADB)
	ResultConstraint = int(C.SQLITE_CONSTRAINT)
)

// Column value classes, as returned by Stmt.ColumnType.
const (
	TypeInteger = int(C.SQLITE_INTEGER)
	TypeFloat   = int(C.SQLITE_FLOAT)
	TypeText    = int(C.SQLITE_TEXT)
	TypeBlob    = int(C.SQLITE_BLOB)
	TypeNull    = int(C.SQLITE_NULL)
)

// Sentinels matched by errors.Is through *Error.Unwrap.
var (
	ErrBusy     = errors.New("sqlite: database is locked")
	ErrLocked   = errors.New("sqlite: database table is locked")
	ErrReadOnly = errors.New("sqlite: attempt to write a readonly database")
	ErrCorrupt  = errors.New("sqlite: database disk image is malformed")
	ErrCantOpen = errors.New("sqlite: unable to open database file")
	ErrNotADB   = errors.New("sqlite: file is not a database")
	// ErrNull reports a NULL where a typed read expected a value.
	ErrNull = errors.New("sqlite: unexpected NULL")
)

// Error is one SQLite failure: the result code plus the connection's message
// at the time of the call.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return "sqlite: error " + strconv.Itoa(e.Code)
	}
	return "sqlite: " + e.Msg + " (code " + strconv.Itoa(e.Code) + ")"
}

// Unwrap maps the primary result code to a sentinel so errors.Is works.
func (e *Error) Unwrap() error {
	switch e.Code & 0xff {
	case ResultBusy:
		return ErrBusy
	case ResultLocked:
		return ErrLocked
	case ResultReadOnly:
		return ErrReadOnly
	case ResultCorrupt:
		return ErrCorrupt
	case ResultCantOpen:
		return ErrCantOpen
	case ResultNotADB:
		return ErrNotADB
	default:
		return nil
	}
}

// Conn is one SQLite connection. It is not safe for concurrent use; fastdb
// documents one Conn per goroutine.
type Conn struct {
	db *C.sqlite3
}

// OpenReadOnly opens path with SQLITE_OPEN_READONLY. Paths beginning with
// "file:" are treated as URIs. SQLite opens lazily, so a missing file is
// reported here while a malformed database is reported by the first statement
// (fastdb.OpenReadOnly runs its pragmas immediately).
func OpenReadOnly(path string) (*Conn, error) {
	flags := C.int(C.SQLITE_OPEN_READONLY | C.SQLITE_OPEN_NOMUTEX)
	if strings.HasPrefix(path, "file:") {
		flags |= C.SQLITE_OPEN_URI
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var db *C.sqlite3
	if rc := C.sqlite3_open_v2(cpath, &db, flags, nil); rc != C.SQLITE_OK {
		err := openError(rc, db)
		if db != nil {
			C.sqlite3_close_v2(db)
		}
		return nil, err
	}
	return &Conn{db: db}, nil
}

// Close finalizes nothing; fastdb finalizes cached statements first. Close is
// idempotent.
func (c *Conn) Close() error {
	if c.db == nil {
		return nil
	}
	rc := C.sqlite3_close_v2(c.db)
	c.db = nil
	if rc != C.SQLITE_OK {
		return &Error{Code: int(rc), Msg: C.GoString(C.sqlite3_errstr(rc))}
	}
	return nil
}

// BusyTimeout sets how long SQLite retries a locked database before returning
// SQLITE_BUSY.
func (c *Conn) BusyTimeout(ms int) error {
	return c.err(C.sqlite3_busy_timeout(c.db, C.int(ms)))
}

// Errmsg returns the connection's current error text.
func (c *Conn) Errmsg() string {
	return C.GoString(C.sqlite3_errmsg(c.db))
}

// Changes returns the rows changed by the most recent statement. A read-only
// connection always reports 0; the call exists for callers that reuse the
// binding for writes later.
func (c *Conn) Changes() int {
	return int(C.sqlite3_changes(c.db))
}

// Prepare compiles one statement. The SQL text is copied to C memory for the
// duration of the call only; sqlite3_prepare_v2 stores its own copy.
func (c *Conn) Prepare(sql string) (*Stmt, error) {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))
	var st *C.sqlite3_stmt
	rc := C.sqlite3_prepare_v2(c.db, csql, C.int(len(sql)), &st, nil)
	if rc != C.SQLITE_OK {
		return nil, c.err(rc)
	}
	return &Stmt{conn: c, stmt: st}, nil
}

// Exec runs a statement for effect, stepping to completion. It is used for
// the connection pragmas.
func (c *Conn) Exec(sql string) error {
	st, err := c.Prepare(sql)
	if err != nil {
		return err
	}
	for {
		row, err := st.Step()
		if err != nil {
			st.Reset()
			st.Finalize()
			return err
		}
		if !row {
			break
		}
	}
	if err := st.Reset(); err != nil {
		st.Finalize()
		return err
	}
	return st.Finalize()
}

func (c *Conn) err(rc C.int) error {
	if rc == C.SQLITE_OK {
		return nil
	}
	if c.db != nil {
		return &Error{Code: int(rc), Msg: C.GoString(C.sqlite3_errmsg(c.db))}
	}
	return &Error{Code: int(rc), Msg: C.GoString(C.sqlite3_errstr(rc))}
}

func openError(rc C.int, db *C.sqlite3) error {
	msg := ""
	if db != nil {
		msg = C.GoString(C.sqlite3_errmsg(db))
	}
	if msg == "" {
		msg = C.GoString(C.sqlite3_errstr(rc))
	}
	return &Error{Code: int(rc), Msg: msg}
}

// Stmt is one prepared statement. It is not safe for concurrent use.
type Stmt struct {
	conn *Conn
	stmt *C.sqlite3_stmt
}

// Step advances one row. It reports false at SQLITE_DONE and returns a typed
// error for any other failure.
func (s *Stmt) Step() (bool, error) {
	switch rc := C.sqlite3_step(s.stmt); rc {
	case C.SQLITE_ROW:
		return true, nil
	case C.SQLITE_DONE:
		return false, nil
	default:
		return false, s.conn.err(rc)
	}
}

// BindInt64 binds parameter i (1-based).
func (s *Stmt) BindInt64(i int, v int64) error {
	return s.conn.err(C.sqlite3_bind_int64(s.stmt, C.int(i), C.sqlite3_int64(v)))
}

// BindText binds parameter i (1-based). SQLite copies the bytes during the
// call (SQLITE_TRANSIENT); the Go string is not retained.
func (s *Stmt) BindText(i int, v string) error {
	var p unsafe.Pointer
	if len(v) > 0 {
		p = unsafe.Pointer(unsafe.StringData(v))
	}
	rc := C.fastdb_bind_text(s.stmt, C.int(i), p, C.int(len(v)))
	runtime.KeepAlive(v)
	return s.conn.err(rc)
}

// BindTextBytes binds parameter i (1-based) from a byte slice with the same
// copy-on-bind rule as BindText.
func (s *Stmt) BindTextBytes(i int, v []byte) error {
	var p unsafe.Pointer
	if len(v) > 0 {
		p = unsafe.Pointer(unsafe.SliceData(v))
	}
	rc := C.fastdb_bind_text(s.stmt, C.int(i), p, C.int(len(v)))
	runtime.KeepAlive(v)
	return s.conn.err(rc)
}

// ClearBindings resets all parameters to NULL.
func (s *Stmt) ClearBindings() error {
	return s.conn.err(C.sqlite3_clear_bindings(s.stmt))
}

// Reset ends the current execution, leaving the statement reusable. The
// returned error is the result of the most recent Step; callers that already
// handled a step error drop it.
func (s *Stmt) Reset() error {
	return s.conn.err(C.sqlite3_reset(s.stmt))
}

// Finalize destroys the statement. The receiver must not be used afterwards.
func (s *Stmt) Finalize() error {
	if s.stmt == nil {
		return nil
	}
	rc := C.sqlite3_finalize(s.stmt)
	s.stmt = nil
	if rc != C.SQLITE_OK {
		return &Error{Code: int(rc), Msg: C.GoString(C.sqlite3_errstr(rc))}
	}
	return nil
}

// ColumnCount returns the number of result columns.
func (s *Stmt) ColumnCount() int {
	return int(C.sqlite3_column_count(s.stmt))
}

// ColumnType returns the value class of column i (0-based).
func (s *Stmt) ColumnType(i int) int {
	return int(C.sqlite3_column_type(s.stmt, C.int(i)))
}

// ColumnInt64 returns column i as an integer.
func (s *Stmt) ColumnInt64(i int) int64 {
	return int64(C.sqlite3_column_int64(s.stmt, C.int(i)))
}

// ColumnBytes returns a view of column i's bytes. The view aliases SQLite's
// buffer for the current row: it is valid only until the next Step, Reset or
// Finalize. A zero-length or NULL value returns nil; ColumnType distinguishes
// them. The caller must not retain the view.
func (s *Stmt) ColumnBytes(i int) []byte {
	n := C.sqlite3_column_bytes(s.stmt, C.int(i))
	if n == 0 {
		return nil
	}
	p := C.sqlite3_column_text(s.stmt, C.int(i))
	if p == nil {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), int(n))
}

// ColumnText returns column i as a freshly allocated Go string. NULL is an
// error; an empty string is not.
func (s *Stmt) ColumnText(i int) (string, error) {
	if C.sqlite3_column_type(s.stmt, C.int(i)) == C.SQLITE_NULL {
		return "", ErrNull
	}
	n := C.sqlite3_column_bytes(s.stmt, C.int(i))
	if n == 0 {
		return "", nil
	}
	p := C.sqlite3_column_text(s.stmt, C.int(i))
	return C.GoStringN((*C.char)(unsafe.Pointer(p)), n), nil
}
