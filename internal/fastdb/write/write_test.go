package write

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3" // links the amalgamation csqlite calls into the test binary

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

// schemaStatements are the minimal relational surface these tests write to
// (the production schema is applied by internal/database; the write
// connection itself is schema-agnostic). Each entry is one statement:
// sqlite3_prepare_v2 compiles a single statement, so multi-statement text
// would silently drop the tail.
var schemaStatements = [...]string{
	"CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, a INTEGER, b TEXT)",
	"CREATE TABLE u (id INTEGER PRIMARY KEY AUTOINCREMENT, x TEXT UNIQUE)",
}

func openTest(t *testing.T) *WriteConn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "write.sqlite3")
	conn, err := OpenWriter(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	// Apply the schema in one transaction, as the database layer does.
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range schemaStatements {
		if err := tx.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return conn
}

// execOne runs query with the given binds to completion.
func execOne(t *testing.T, tx *WriteTx, query string, bind func(s *WriteStmt) error) error {
	t.Helper()
	s, err := tx.Stmt(query)
	if err != nil {
		return err
	}
	defer s.Reset()
	if bind != nil {
		if err := bind(s); err != nil {
			return err
		}
	}
	if row, err := s.Step(); err != nil {
		return err
	} else if row {
		t.Fatalf("%s returned a row", query)
	}
	return nil
}

func TestOpenWriterPragmas(t *testing.T) {
	conn := openTest(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	check := func(query, want string) {
		t.Helper()
		s, err := tx.Stmt(query)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Reset()
		row, err := s.Step()
		if err != nil || !row {
			t.Fatalf("%s: %v %v", query, row, err)
		}
		got, err := s.Text(0)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	check("PRAGMA journal_mode", "wal")
	check("PRAGMA synchronous", "1")
	check("PRAGMA wal_autocheckpoint", "0")
	check("PRAGMA busy_timeout", "5000")
	check("PRAGMA foreign_keys", "1")
	check("PRAGMA cache_size", "2000")
}

func TestBeginCommitRollback(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (1,'x')", nil); err != nil {
		t.Fatal(err)
	}
	if tx.LastInsertID() != 1 {
		t.Fatalf("last insert id = %d, want 1", tx.LastInsertID())
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A double-finalize reports the same sentinel database/sql reports.
	if err := tx.Commit(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("second commit: %v, want ErrTxDone", err)
	}
	if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("rollback after commit: %v, want ErrTxDone", err)
	}

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (2,'y')", nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("second rollback: %v, want ErrTxDone", err)
	}
	// The rollback persisted nothing; the earlier commit did.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	s, err := tx.Stmt("SELECT count(*),sum(a) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	if row, err := s.Step(); err != nil || !row {
		t.Fatalf("count step: %v %v", row, err)
	}
	n, err := s.Int64(0)
	if err != nil {
		t.Fatal(err)
	}
	total, err := s.Int64(1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || total != 1 {
		t.Fatalf("rows = %d sum = %d, want 1 and 1", n, total)
	}
}

func TestStatementCacheReuse(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := int64(1); i <= 3; i++ {
		if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (?,?)", func(s *WriteStmt) error {
			if err := s.BindInt64(1, i); err != nil {
				return err
			}
			return s.BindText(2, "x")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(conn.stmts) == 0 {
		t.Fatal("no statements cached")
	}
	cached := len(conn.stmts)
	for i := int64(4); i <= 6; i++ {
		if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (?,?)", func(s *WriteStmt) error {
			if err := s.BindInt64(1, i); err != nil {
				return err
			}
			return s.BindText(2, "x")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(conn.stmts) != cached {
		t.Fatalf("statement cache grew on reuse: %d -> %d", cached, len(conn.stmts))
	}
}

func TestBindKindsAndNull(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (?,?)", func(s *WriteStmt) error {
		if err := s.BindInt64(1, 7); err != nil {
			return err
		}
		return s.BindNull(2)
	}); err != nil {
		t.Fatal(err)
	}
	s, err := tx.Stmt("SELECT a,b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	if row, err := s.Step(); err != nil || !row {
		t.Fatalf("step: %v %v", row, err)
	}
	if v, err := s.Int64(0); err != nil || v != 7 {
		t.Fatalf("a = %d %v, want 7", v, err)
	}
	if !s.IsNull(1) {
		t.Fatal("b not NULL")
	}
	if _, err := s.Text(1); err != csqlite.ErrNull {
		t.Fatalf("Text(NULL) = %v, want ErrNull", err)
	}
	if _, err := s.Int64(1); err == nil {
		t.Fatal("Int64(NULL) accepted")
	}
}

func TestConstraintAndPrepareErrors(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := execOne(t, tx, "INSERT INTO u(x) VALUES ('dup')", nil); err != nil {
		t.Fatal(err)
	}
	err = execOne(t, tx, "INSERT INTO u(x) VALUES ('dup')", nil)
	if err == nil || !errors.Is(err, csqlite.ErrConstraint) {
		t.Fatalf("unique violation: %v, want a constraint error", err)
	}
	// A prepare failure (missing table) is not cached and fails every use.
	err = execOne(t, tx, "INSERT INTO missing(x) VALUES (1)", nil)
	if err == nil {
		t.Fatal("insert into missing table succeeded")
	}
	err = execOne(t, tx, "INSERT INTO missing(x) VALUES (1)", nil)
	if err == nil {
		t.Fatal("second insert into missing table succeeded")
	}
}

func TestNoRows(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	s, err := tx.Stmt("SELECT a FROM t WHERE a=99")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	row, err := s.Step()
	if err != nil {
		t.Fatal(err)
	}
	if row {
		t.Fatal("unexpected row")
	}
}

func TestChanges(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := int64(1); i <= 3; i++ {
		if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (?, 'x')", func(s *WriteStmt) error {
			return s.BindInt64(1, i)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := tx.Changes(); got != 1 {
		t.Fatalf("changes after one insert = %d, want 1", got)
	}
	if err := execOne(t, tx, "UPDATE t SET b='y' WHERE a>=2", nil); err != nil {
		t.Fatal(err)
	}
	if got := tx.Changes(); got != 2 {
		t.Fatalf("changes after update = %d, want 2", got)
	}
}

// TestBusyMapping pins the error mapping for a locked database: a second
// writer whose begin cannot get the write lock within the busy timeout
// reports an error matching the csqlite busy sentinel, not a hang.
func TestBusyMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.sqlite3")
	conn, err := OpenWriter(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	other, err := OpenWriter(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.db.BusyTimeout(20); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range schemaStatements {
		if err := tx.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (1,'x')", nil); err != nil {
		t.Fatal(err)
	}
	// WAL: a writer on another connection waits for the writer lock; the
	// other connection's begin times out on it.
	_, err2 := other.Begin(ctx)
	if err2 == nil {
		t.Fatal("second writer began while the first held the write lock")
	}
	if !errors.Is(err2, csqlite.ErrBusy) {
		t.Fatalf("locked begin: %v, want a busy error", err2)
	}
}

// TestConcurrentBeginSerializes drives many goroutines at Begin/Commit to
// prove the transaction mutex hands the connection to exactly one
// transaction at a time (-race gate).
func TestConcurrentBeginSerializes(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := conn.Begin(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			s, err := tx.Stmt("INSERT INTO t(a,b) VALUES (?, 'x')")
			if err != nil {
				errs[i] = err
				tx.Rollback()
				return
			}
			if err := s.BindInt64(1, int64(i)); err != nil {
				errs[i] = err
				s.Reset()
				tx.Rollback()
				return
			}
			if row, err := s.Step(); err != nil {
				errs[i] = err
			} else if row {
				errs[i] = errors.New("insert returned a row")
			}
			s.Reset()
			if errs[i] == nil {
				errs[i] = tx.Commit()
			}
			if errs[i] != nil {
				tx.Rollback()
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	s, err := tx.Stmt("SELECT count(*) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	if row, err := s.Step(); err != nil || !row {
		t.Fatalf("step: %v %v", row, err)
	}
	n, err := s.Int64(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 32 {
		t.Fatalf("rows = %d, want 32", n)
	}
}

func TestBeginCancelledContext(t *testing.T) {
	conn := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conn.Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("begin with cancelled ctx: %v, want context.Canceled", err)
	}
}

func TestCloseShapes(t *testing.T) {
	conn := openTest(t)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := conn.Begin(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("begin after close: %v", err)
	}
}

func TestSavepointStatements(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The batch lane's statements: one job in a savepoint, rolled back; a
	// second job retained.
	if err := tx.Exec("SAVEPOINT w"); err != nil {
		t.Fatal(err)
	}
	if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (1,'x')", nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("ROLLBACK TO w"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("RELEASE w"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("SAVEPOINT w"); err != nil {
		t.Fatal(err)
	}
	if err := execOne(t, tx, "INSERT INTO t(a,b) VALUES (2,'y')", nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("RELEASE w"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	s, err := tx.Stmt("SELECT a FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	var got []int64
	for {
		row, err := s.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !row {
			break
		}
		v, err := s.Int64(0)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("rows after savepoint dance = %v, want [2]", got)
	}
}
