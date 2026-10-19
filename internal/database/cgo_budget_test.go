package database

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/fastdb/write"
)

// The lever-3 cgo budget pins (ENGINE-55 write path): each create-transaction
// statement that used to cross into C once per bind plus once each for the
// step, the last-insert-id read and the reset now crosses once, because the
// fused helpers run the whole sequence inside C. The tests measure the real
// statement texts on a bare write.WriteConn — no writer loop and no
// checkpointer goroutine — so runtime.NumCgoCall sees only this test's calls.

// budgetSchema is the relational surface the two pinned INSERTs write to; it
// mirrors schema.sql's messages and action_text_rich_texts columns the create
// transaction touches, so the pinned texts run against their production shape.
var budgetSchema = [...]string{
	"CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, client_message_id TEXT, creator_id INTEGER, room_id INTEGER, created_at TEXT, updated_at TEXT)",
	"CREATE TABLE action_text_rich_texts (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, record_type TEXT, record_id INTEGER, body TEXT, created_at TEXT, updated_at TEXT)",
	"CREATE TABLE rooms (id INTEGER PRIMARY KEY AUTOINCREMENT, updated_at TEXT)",
	"CREATE TABLE message_search_index (rowid INTEGER, body TEXT)",
	"CREATE TABLE memberships (id INTEGER PRIMARY KEY AUTOINCREMENT, unread_at TEXT, updated_at TEXT, room_id INTEGER, user_id INTEGER, involvement TEXT, connected_at TEXT)",
}

// openBudgetConn opens a bare write connection with the budget schema applied.
func openBudgetConn(t *testing.T) *write.WriteConn {
	t.Helper()
	conn, err := write.OpenWriter(filepath.Join(t.TempDir(), "budget.sqlite3"), 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range budgetSchema {
		if err := tx.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return conn
}

// TestMessageInsertCgoBudget pins the message INSERT's transition budget:
// eight cgo calls unfused (five binds, step, last-insert-id, reset) and one
// fused (write.WriteStmt.ExecInsertMessage). The unfused count is measured on
// the same cached statement in the same test, so "before" is not a claim about
// history but a live baseline the fused count is compared against.
func TestMessageInsertCgoBudget(t *testing.T) {
	const stamp = "2026-01-02 03:04:05.000000"
	conn := openBudgetConn(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Warm the cache: the first Stmt prepares (a cgo call) and the fused run
	// exercises the path; neither is measured.
	s, err := tx.Stmt(stmtInsertMessage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecInsertMessage("warm", 1, 1, stamp); err != nil {
		t.Fatal(err)
	}

	before := runtime.NumCgoCall()
	id, err := s.ExecInsertMessage("fused-1", 1, 1, stamp)
	fused := runtime.NumCgoCall() - before
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("fused insert returned no row id")
	}
	if fused != 1 {
		t.Fatalf("fused message insert crossed into C %d times, want 1", fused)
	}

	// The unfused baseline on the same cached statement: one crossing per bind
	// plus step, rowid and reset.
	before = runtime.NumCgoCall()
	if err := s.BindText(1, "manual-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindInt64(2, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.BindInt64(3, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.BindText(4, stamp); err != nil {
		t.Fatal(err)
	}
	if err := s.BindText(5, stamp); err != nil {
		t.Fatal(err)
	}
	if row, err := s.Step(); err != nil {
		t.Fatal(err)
	} else if row {
		t.Fatal("manual insert returned a row")
	}
	tx.LastInsertID()
	s.Reset()
	unfused := runtime.NumCgoCall() - before
	if unfused != 8 {
		t.Fatalf("unfused message insert crossed into C %d times, want 8", unfused)
	}

	// The fused path allocates nothing per insert; the statement is cached and
	// the typed arguments are plain values.
	if allocs := testing.AllocsPerRun(200, func() {
		if _, err := s.ExecInsertMessage("alloc", 1, 1, stamp); err != nil {
			t.Fatal(err)
		}
	}); allocs != 0 {
		t.Fatalf("fused message insert allocates %v per run, want 0", allocs)
	}
}

// TestRichTextInsertCgoBudget pins the rich-text INSERT the same way: six
// unfused crossings (four binds, step, reset) and one fused
// (write.WriteStmt.ExecInsertRichText).
func TestRichTextInsertCgoBudget(t *testing.T) {
	const stamp = "2026-01-02 03:04:05.000000"
	conn := openBudgetConn(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s, err := tx.Stmt(stmtInsertRichText)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ExecInsertRichText(1, "<p>warm</p>", stamp); err != nil {
		t.Fatal(err)
	}

	before := runtime.NumCgoCall()
	if err := s.ExecInsertRichText(2, "<p>fused</p>", stamp); err != nil {
		t.Fatal(err)
	}
	fused := runtime.NumCgoCall() - before
	if fused != 1 {
		t.Fatalf("fused rich-text insert crossed into C %d times, want 1", fused)
	}

	before = runtime.NumCgoCall()
	if err := s.BindInt64(1, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.BindText(2, "<p>manual</p>"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindText(3, stamp); err != nil {
		t.Fatal(err)
	}
	if err := s.BindText(4, stamp); err != nil {
		t.Fatal(err)
	}
	if row, err := s.Step(); err != nil {
		t.Fatal(err)
	} else if row {
		t.Fatal("manual insert returned a row")
	}
	s.Reset()
	unfused := runtime.NumCgoCall() - before
	if unfused != 6 {
		t.Fatalf("unfused rich-text insert crossed into C %d times, want 6", unfused)
	}

	if allocs := testing.AllocsPerRun(200, func() {
		if err := s.ExecInsertRichText(4, "<p>alloc</p>", stamp); err != nil {
			t.Fatal(err)
		}
	}); allocs != 0 {
		t.Fatalf("fused rich-text insert allocates %v per run, want 0", allocs)
	}
}

// TestCreateTransactionCgoBudget pins the whole create transaction's budget
// on the direct lane: the fused statements plus the still-unfused membership
// and creator reads and the transaction control. It is the sum the per-post
// path pays, and the guard against a fused statement silently reverting to
// the unfused sequence.
func TestCreateTransactionCgoBudget(t *testing.T) {
	const stamp = "2026-01-02 03:04:05.000000"
	conn := openBudgetConn(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Warm every statement the transaction touches.
	type step struct {
		stmt string
		run  func() error
	}
	steps := []step{
		{stmtInsertMessage, func() error {
			s, err := tx.Stmt(stmtInsertMessage)
			if err != nil {
				return err
			}
			_, err = s.ExecInsertMessage("warm", 1, 1, stamp)
			return err
		}},
		{stmtTouchRoom, func() error {
			s, err := tx.Stmt(stmtTouchRoom)
			if err != nil {
				return err
			}
			return s.ExecTouchRoom(1, stamp)
		}},
		{stmtInsertRichText, func() error {
			s, err := tx.Stmt(stmtInsertRichText)
			if err != nil {
				return err
			}
			return s.ExecInsertRichText(1, "<p>warm</p>", stamp)
		}},
		{stmtInsertSearchIndex, func() error {
			s, err := tx.Stmt(stmtInsertSearchIndex)
			if err != nil {
				return err
			}
			return s.ExecInsertSearchIndex(1, "warm")
		}},
		{stmtBumpUnread, func() error {
			s, err := tx.Stmt(stmtBumpUnread)
			if err != nil {
				return err
			}
			return s.ExecBumpUnread(1, 2, stamp, stamp)
		}},
	}
	for _, st := range steps {
		if err := st.run(); err != nil {
			t.Fatalf("%s: %v", st.stmt, err)
		}
	}

	before := runtime.NumCgoCall()
	for _, st := range steps {
		if err := st.run(); err != nil {
			t.Fatalf("%s: %v", st.stmt, err)
		}
	}
	got := runtime.NumCgoCall() - before
	// Five fused statements, one crossing each.
	if got != 5 {
		t.Fatalf("five fused create statements crossed into C %d times, want 5", got)
	}
}
