package fastdb

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

func TestOpenReadOnlyPragmas(t *testing.T) {
	path := fixtureDB(t)
	c, err := OpenReadOnly(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, tc := range []struct {
		query string
		want  int64
	}{
		{"PRAGMA busy_timeout", 5000},
		{"PRAGMA foreign_keys", 1},
		{"PRAGMA query_only", 1},
		{"PRAGMA synchronous", 1}, // NORMAL
		{"PRAGMA cache_size", 2000},
	} {
		if got := queryInt(t, c, tc.query); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}
	if got := queryText(t, c, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("PRAGMA journal_mode = %q, want wal", got)
	}
}

func TestDefaultStatementCap(t *testing.T) {
	path := fixtureDB(t)
	c, err := OpenReadOnly(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.limit != 256 {
		t.Fatalf("default cap = %d, want 256", c.limit)
	}
}

func TestStatementCacheCapAndReuse(t *testing.T) {
	path := fixtureDB(t)
	c, err := OpenReadOnly(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Three distinct query texts against a two-entry cap: the third is
	// prepared per use and finalized after, mirroring read_pool.go.
	var room Room
	var user User
	_ = c.Room(&room, 1<<62, 1<<62)
	_, _ = c.Involvement(1<<62, 1<<62)
	if err := c.SessionUser(&user, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ephemeral statement failed: %v", err)
	}
	if len(c.stmts) != 2 {
		t.Fatalf("cache holds %d statements, want cap 2", len(c.stmts))
	}
	first := c.stmts[queryRoom]
	if first == nil {
		t.Fatal("room statement was not cached")
	}
	_ = c.Room(&room, 1<<62, 1<<62)
	if c.stmts[queryRoom] != first {
		t.Error("cached room statement was re-prepared")
	}
	// The ephemeral query keeps working without entering the cache.
	if err := c.SessionUser(&user, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ephemeral reuse failed: %v", err)
	}
	if len(c.stmts) != 2 {
		t.Fatalf("cache grew to %d, want cap 2", len(c.stmts))
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	path := fixtureDB(t)
	c, err := OpenReadOnly(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// mode=ro and query_only=on are both active; whichever statement phase
	// rejects the write, the error must be SQLITE_READONLY.
	st, err := c.db.Prepare("INSERT INTO rooms(name,type,creator_id,created_at,updated_at) VALUES ('x','Rooms::Open',1,'2026-01-01 00:00:00','2026-01-01 00:00:00')")
	if err == nil {
		_, err = st.Step()
		st.Reset()
		st.Finalize()
	}
	if !errors.Is(err, csqlite.ErrReadOnly) {
		t.Fatalf("write on read-only connection: got %v, want ErrReadOnly", err)
	}
}

func TestOpenReadOnlyErrors(t *testing.T) {
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing.sqlite3"), 0); !errors.Is(err, csqlite.ErrCantOpen) {
		t.Errorf("missing file: got %v, want ErrCantOpen", err)
	}

	notDB := filepath.Join(t.TempDir(), "not-a-database")
	if err := os.WriteFile(notDB, []byte("this is not a sqlite database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(notDB, 0); !errors.Is(err, csqlite.ErrNotADB) {
		t.Errorf("not a database: got %v, want ErrNotADB", err)
	}

	// A rollback-journal database cannot satisfy the reader's WAL pragma on a
	// read-only handle; the database/sql readers fail the same way.
	plain := filepath.Join(t.TempDir(), "plain.sqlite3")
	w, err := sql.Open("sqlite3", "file:"+plain)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Exec("CREATE TABLE t(id integer primary key, x text)"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(plain, 0); !errors.Is(err, csqlite.ErrReadOnly) {
		t.Errorf("rollback journal: got %v, want ErrReadOnly", err)
	}
}

func TestMemoryConn(t *testing.T) {
	c, err := OpenReadOnly(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := queryInt(t, c, "SELECT count(*) FROM sqlite_master"); got != 0 {
		t.Fatalf("sqlite_master count = %d, want 0", got)
	}
	if got := queryText(t, c, "PRAGMA journal_mode"); got != "memory" {
		t.Fatalf("memory journal_mode = %q", got)
	}
}

func TestCloseIdempotent(t *testing.T) {
	path := fixtureDB(t)
	c, err := OpenReadOnly(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestRowsKindsAndNull(t *testing.T) {
	c, err := OpenReadOnly(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	st, err := c.db.Prepare("SELECT 1, 2.5, 'x', NULL")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	rows := Rows{stmt: st}
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	if got := rows.Kind(0); got != KindInteger {
		t.Errorf("kind 0 = %d, want integer", got)
	}
	if got := rows.Int64(0); got != 1 {
		t.Errorf("int64 0 = %d", got)
	}
	if got := rows.Kind(1); got != KindFloat {
		t.Errorf("kind 1 = %d, want float", got)
	}
	if got := rows.Kind(2); got != KindText {
		t.Errorf("kind 2 = %d, want text", got)
	}
	if text, err := rows.Text(2); err != nil || text != "x" {
		t.Errorf("text 2 = %q, %v", text, err)
	}
	if got := rows.Kind(3); got != KindNull || !rows.IsNull(3) {
		t.Errorf("kind 3 = %d null=%v, want null", got, rows.IsNull(3))
	}
	if _, err := rows.Text(3); !errors.Is(err, csqlite.ErrNull) {
		t.Errorf("null text: got %v, want ErrNull", err)
	}
}

func TestColumnTextIntoDefensiveCopy(t *testing.T) {
	c, err := OpenReadOnly(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	st, err := c.db.Prepare("SELECT 'alpha' UNION ALL SELECT 'beta'")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	rows := Rows{stmt: st}

	if !rows.Next() {
		t.Fatalf("no first row: %v", rows.Err())
	}
	first, err := rows.ColumnTextInto(0, nil)
	if err != nil || string(first) != "alpha" {
		t.Fatalf("first = %q, %v", first, err)
	}
	if !rows.Next() {
		t.Fatalf("no second row: %v", rows.Err())
	}
	// If ColumnTextInto returned SQLite's internal buffer, stepping would
	// have overwritten it (or finalizing would invalidate it).
	if string(first) != "alpha" {
		t.Fatalf("first buffer aliases SQLite memory: %q after step", first)
	}
	second, err := rows.ColumnTextInto(0, first[:0])
	if err != nil || string(second) != "beta" {
		t.Fatalf("second = %q, %v", second, err)
	}
	// The caller's buffer is ours to mutate.
	second[0] = 'Z'
	if string(second) != "Zeta" {
		t.Fatalf("mutated buffer = %q", second)
	}
}

// TestOpenBusyTimeoutWaits locks a WAL database from a held exclusive writer
// and asserts fastdb's own pragma sequence waits the configured 5000 ms before
// surfacing SQLITE_BUSY as a typed error. Skipped in -short mode: the wait is
// the assertion.
func TestOpenBusyTimeoutWaits(t *testing.T) {
	if testing.Short() {
		t.Skip("the busy_timeout wait is the assertion")
	}
	path := filepath.Join(t.TempDir(), "exclusive.sqlite3")
	w, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec("CREATE TABLE probe(id integer primary key)"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("INSERT INTO probe(id) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	// In WAL mode an exclusive-locking connection that has written holds the
	// whole-database lock until it closes, so every later reader waits.
	if _, err := w.Exec("PRAGMA locking_mode=EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("INSERT INTO probe(id) VALUES(2)"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	c, err := OpenReadOnly(path, 256)
	elapsed := time.Since(start)
	if c != nil {
		c.Close()
		t.Fatal("expected the open to fail while the exclusive writer holds the lock")
	}
	if !errors.Is(err, csqlite.ErrBusy) {
		t.Fatalf("open: got %v, want ErrBusy", err)
	}
	if elapsed < 4500*time.Millisecond {
		t.Fatalf("open returned after %v; busy_timeout was not honoured", elapsed)
	}
}

func TestWALReaderSeesCommittedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite3")
	// Create the WAL database through the driver directly: the schema-bearing
	// database.Open path needs the sqlite_fts5 build tag, and this test only
	// needs one probe table.
	w, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec("CREATE TABLE probe(id integer primary key, x text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("INSERT INTO probe(id,x) VALUES(1,'committed')"); err != nil {
		t.Fatal(err)
	}

	c, err := OpenReadOnly(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// A held writer transaction does not block WAL readers.
	tx, err := w.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE probe SET x='uncommitted' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if got := probeText(t, c, "SELECT x FROM probe WHERE id=1"); got != "committed" {
		t.Fatalf("dirty read: %q", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := probeText(t, c, "SELECT x FROM probe WHERE id=1"); got != "committed" {
		t.Fatalf("after rollback: %q", got)
	}

	tx, err = w.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE probe SET x='second' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := probeText(t, c, "SELECT x FROM probe WHERE id=1"); got != "second" {
		t.Fatalf("after commit: %q", got)
	}
}

// TestBusyTimeoutWaitsAndSurfaces exercises the busy handler directly: a
// rollback-journal database with a held EXCLUSIVE writer makes a fresh
// statement wait for busy_timeout before returning SQLITE_BUSY. (WAL readers
// do not block on writers, so this scenario is the deterministic busy case;
// fastdb's 5000 ms value is asserted in TestOpenReadOnlyPragmas.)
func TestBusyTimeoutWaitsAndSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.sqlite3")
	w, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec("CREATE TABLE probe(id integer primary key, x text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("INSERT INTO probe(id,x) VALUES(1,'held')"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("UPDATE probe SET x='writer' WHERE id=1"); err != nil {
		t.Fatal(err)
	}

	c, err := csqlite.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if rc := c.BusyTimeout(250); rc != nil {
		t.Fatal(rc)
	}

	start := time.Now()
	st, err := c.Prepare("SELECT x FROM probe WHERE id=1")
	if err == nil {
		_, err = st.Step()
		st.Reset()
		st.Finalize()
	}
	elapsed := time.Since(start)
	var serr *csqlite.Error
	if !errors.As(err, &serr) || serr.Code != csqlite.ResultBusy {
		t.Fatalf("held writer: got %v, want SQLITE_BUSY", err)
	}
	if !errors.Is(err, csqlite.ErrBusy) {
		t.Fatalf("errors.Is(err, ErrBusy) = false for %v", err)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("returned after %v; busy_timeout was not honoured", elapsed)
	}

	if _, err := w.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	st, err = c.Prepare("SELECT x FROM probe WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	if row, err := st.Step(); err != nil || !row {
		t.Fatalf("after rollback: row=%v err=%v", row, err)
	}
	if got, err := st.ColumnText(0); err != nil || got != "held" {
		t.Fatalf("after rollback: %q, %v", got, err)
	}
}

func queryInt(t *testing.T, c *Conn, query string) int64 {
	t.Helper()
	st, err := c.db.Prepare(query)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	row, err := st.Step()
	if err != nil || !row {
		t.Fatalf("%s: row=%v err=%v", query, row, err)
	}
	return st.ColumnInt64(0)
}

func queryText(t *testing.T, c *Conn, query string) string {
	t.Helper()
	st, err := c.db.Prepare(query)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	row, err := st.Step()
	if err != nil || !row {
		t.Fatalf("%s: row=%v err=%v", query, row, err)
	}
	text, err := st.ColumnText(0)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func probeText(t *testing.T, c *Conn, query string) string {
	t.Helper()
	st, err := c.db.Prepare(query)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	row, err := st.Step()
	if err != nil || !row {
		t.Fatalf("%s: row=%v err=%v", query, row, err)
	}
	text, err := st.ColumnText(0)
	if err != nil {
		t.Fatal(err)
	}
	return text
}
