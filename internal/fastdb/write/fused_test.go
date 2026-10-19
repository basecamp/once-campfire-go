package write

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

// The fused write shapes must produce exactly what the unfused bind/step/reset
// sequence produced: same rows, same returned rowid, same error mapping, same
// reset-on-every-path reuse. These tests hold ExecTyped to that, plus the
// ownership rule that SQLite copies bound text during the call
// (SQLITE_TRANSIENT) rather than retaining a Go pointer.

// execTyped adapts the fixed five-slot ExecTyped to the variable argument
// lists these tests use; unused slots stay ArgSkip.
func execTyped(t *testing.T, s *WriteStmt, wantLastID bool, args ...csqlite.Arg) (int64, bool, error) {
	t.Helper()
	var a [5]csqlite.Arg
	copy(a[:], args)
	return s.st.ExecTyped(wantLastID, a[0], a[1], a[2], a[3], a[4])
}

// dumpTable renders every row of the named five-column message-shaped table as
// one comparable string; NULLs render "<null>" so a NULL and an empty string
// stay distinguishable.
func dumpTable(t *testing.T, tx *WriteTx, table string) string {
	t.Helper()
	s, err := tx.Stmt("SELECT id,client,creator,room,created,updated FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Reset()
	var out strings.Builder
	for {
		row, err := s.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !row {
			break
		}
		id, err := s.Int64(0)
		if err != nil {
			t.Fatal(err)
		}
		creator, err := s.Int64(2)
		if err != nil {
			t.Fatal(err)
		}
		room, err := s.Int64(3)
		if err != nil {
			t.Fatal(err)
		}
		text := func(i int) string {
			if s.IsNull(i) {
				return "<null>"
			}
			v, err := s.Text(i)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		fmt.Fprintf(&out, "%d|%s|%d|%d|%s|%s\n", id, text(1), creator, room, text(4), text(5))
	}
	return out.String()
}

// msgDDL and msgInsert give a table the message INSERT's production shape; the
// table name is spliced in, so the fused and manual runs share one statement
// shape.
const (
	msgDDL    = "CREATE TABLE %s (id INTEGER PRIMARY KEY AUTOINCREMENT, client TEXT, creator INTEGER, room INTEGER, created TEXT, updated TEXT)"
	msgInsert = "INSERT INTO %s(client,creator,room,created,updated) VALUES (?,?,?,?,?)"
)

// TestFusedMatchesUnfused drives the same argument tuples through the fused
// message INSERT and the unfused bind/step/reset sequence on two tables and
// requires the returned ids and the resulting rows to be identical.
func TestFusedMatchesUnfused(t *testing.T) {
	conn := openTest(t)
	ctx := context.Background()

	type tuple struct {
		client string
		user   int64
		room   int64
		stamp  string
	}
	tuples := []tuple{
		{"c-1", 7, 3, "2026-01-02 03:04:05.000000"},
		{"", 0, 0, ""}, // empty text binds '' (not NULL) on both paths
		{"c-2", -5, 1 << 40, "2026-01-02 03:04:05.000000"},
	}

	fused := func(tx *WriteTx, table string) []int64 {
		ids := make([]int64, len(tuples))
		for i, a := range tuples {
			s, err := tx.Stmt(fmt.Sprintf(msgInsert, table))
			if err != nil {
				t.Fatal(err)
			}
			id, _, err := execTyped(t, s, true,
				csqlite.Arg{Kind: csqlite.ArgText, Text: a.client},
				csqlite.Arg{Kind: csqlite.ArgInt, Int: a.user},
				csqlite.Arg{Kind: csqlite.ArgInt, Int: a.room},
				csqlite.Arg{Kind: csqlite.ArgText, Text: a.stamp},
				csqlite.Arg{Kind: csqlite.ArgText, Text: a.stamp},
			)
			if err != nil {
				t.Fatal(err)
			}
			ids[i] = id
		}
		return ids
	}
	manual := func(tx *WriteTx, table string) []int64 {
		ids := make([]int64, len(tuples))
		for i, a := range tuples {
			s, err := tx.Stmt(fmt.Sprintf(msgInsert, table))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.BindText(1, a.client); err != nil {
				t.Fatal(err)
			}
			if err := s.BindInt64(2, a.user); err != nil {
				t.Fatal(err)
			}
			if err := s.BindInt64(3, a.room); err != nil {
				t.Fatal(err)
			}
			if err := s.BindText(4, a.stamp); err != nil {
				t.Fatal(err)
			}
			if err := s.BindText(5, a.stamp); err != nil {
				t.Fatal(err)
			}
			if row, err := s.Step(); err != nil {
				t.Fatal(err)
			} else if row {
				t.Fatal("manual insert returned a row")
			}
			ids[i] = tx.LastInsertID()
			s.Reset()
		}
		return ids
	}

	// Fused into ft.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(fmt.Sprintf(msgDDL, "ft")); err != nil {
		t.Fatal(err)
	}
	fusedIDs := fused(tx, "ft")
	fusedDump := dumpTable(t, tx, "ft")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// The same tuples through the manual sequence, in a second table.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(fmt.Sprintf(msgDDL, "mt")); err != nil {
		t.Fatal(err)
	}
	manualIDs := manual(tx, "mt")
	manualDump := dumpTable(t, tx, "mt")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(fusedIDs) != fmt.Sprint(manualIDs) {
		t.Fatalf("fused ids %v, manual ids %v", fusedIDs, manualIDs)
	}
	if fusedIDs[0] != 1 || fusedIDs[1] != 2 || fusedIDs[2] != 3 {
		t.Fatalf("fused ids = %v, want [1 2 3]", fusedIDs)
	}
	if fusedDump != manualDump {
		t.Fatalf("fused rows:\n%s\nmanual rows:\n%s", fusedDump, manualDump)
	}
	want := "1|c-1|7|3|2026-01-02 03:04:05.000000|2026-01-02 03:04:05.000000\n" +
		"2||0|0||\n" +
		"3|c-2|-5|1099511627776|2026-01-02 03:04:05.000000|2026-01-02 03:04:05.000000\n"
	if fusedDump != want {
		t.Fatalf("fused rows:\n%s\nwant:\n%s", fusedDump, want)
	}
}

// TestFusedNullAndEmptyText pins the two text edges the fused binder must keep:
// ArgNull binds SQL NULL, and an empty ArgText binds an empty string rather
// than NULL (fastdb_bind_text's NULL-with-zero-length rule).
func TestFusedNullAndEmptyText(t *testing.T) {
	conn := openTest(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s, err := tx.Stmt("INSERT INTO t(a,b) VALUES (?,?)")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := execTyped(t, s, false,
		csqlite.Arg{Kind: csqlite.ArgInt, Int: 1},
		csqlite.Arg{Kind: csqlite.ArgNull},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execTyped(t, s, false,
		csqlite.Arg{Kind: csqlite.ArgInt, Int: 2},
		csqlite.Arg{Kind: csqlite.ArgText, Text: ""},
	); err != nil {
		t.Fatal(err)
	}

	read, err := tx.Stmt("SELECT a,b FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Reset()

	if row, err := read.Step(); err != nil || !row {
		t.Fatalf("first row: %v %v", row, err)
	}
	if !read.IsNull(1) {
		t.Fatal("ArgNull did not bind SQL NULL")
	}
	if row, err := read.Step(); err != nil || !row {
		t.Fatalf("second row: %v %v", row, err)
	}
	if read.IsNull(1) {
		t.Fatal("empty ArgText bound NULL, want an empty string")
	}
	got, err := read.Text(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("empty ArgText = %q, want %q", got, "")
	}
}

// TestFusedErrorMappingAndReuse pins the error semantics: a unique violation
// surfaces the same csqlite constraint sentinel as the unfused path, and the
// statement is reset on the failure path so the next job reuses it.
func TestFusedErrorMappingAndReuse(t *testing.T) {
	conn := openTest(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s, err := tx.Stmt("INSERT INTO u(x) VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := execTyped(t, s, false,
		csqlite.Arg{Kind: csqlite.ArgText, Text: "dup"},
	); err != nil {
		t.Fatal(err)
	}
	_, _, err = execTyped(t, s, false,
		csqlite.Arg{Kind: csqlite.ArgText, Text: "dup"},
	)
	if err == nil || !errors.Is(err, csqlite.ErrConstraint) {
		t.Fatalf("duplicate insert: %v, want a constraint error", err)
	}
	// The failed statement was reset, so it runs again for a new value.
	if _, _, err := execTyped(t, s, false,
		csqlite.Arg{Kind: csqlite.ArgText, Text: "fresh"},
	); err != nil {
		t.Fatalf("reuse after constraint failure: %v", err)
	}

	read, err := tx.Stmt("SELECT count(*) FROM u")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Reset()
	if row, err := read.Step(); err != nil || !row {
		t.Fatalf("count: %v %v", row, err)
	}
	n, err := read.Int64(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("u rows = %d, want 2", n)
	}
}

// TestFusedUnexpectedRow pins the row-returned signal: a fused step that yields
// a row reports row=true and a nil error, so the lane can raise its own
// per-statement error, and the statement is still reusable.
func TestFusedUnexpectedRow(t *testing.T) {
	conn := openTest(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s, err := tx.Stmt("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	_, row, err := execTyped(t, s, false, csqlite.Arg{})
	if err != nil {
		t.Fatal(err)
	}
	if !row {
		t.Fatal("ExecTyped on a SELECT reported no row")
	}
	// Reset happened on the row path, so the statement runs again.
	_, row, err = execTyped(t, s, false, csqlite.Arg{})
	if err != nil || !row {
		t.Fatalf("second run: row=%v err=%v", row, err)
	}
}

// TestFusedTextIsCopied supports the SQLITE_TRANSIENT ownership rule: many
// distinct bound strings survive a forced GC and a later reset, which they
// would not if SQLite retained the Go string data (SQLITE_STATIC).
func TestFusedTextIsCopied(t *testing.T) {
	conn := openTest(t)
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	const n = 64
	want := func(i int) string {
		return strings.Repeat(string(rune('a'+i%26)), 64) + fmt.Sprint(i)
	}
	s, err := tx.Stmt("INSERT INTO t(a,b) VALUES (?,?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		// A fresh, non-interned string each iteration.
		if _, _, err := execTyped(t, s, false,
			csqlite.Arg{Kind: csqlite.ArgInt, Int: int64(i)},
			csqlite.Arg{Kind: csqlite.ArgText, Text: want(i)},
		); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.GC()

	read, err := tx.Stmt("SELECT a,b FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Reset()
	for i := 0; i < n; i++ {
		if row, err := read.Step(); err != nil || !row {
			t.Fatalf("row %d: %v %v", i, row, err)
		}
		a, err := read.Int64(0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := read.Text(1)
		if err != nil {
			t.Fatal(err)
		}
		if a != int64(i) || got != want(i) {
			t.Fatalf("row %d = (%d,%q), want (%d,%q)", i, a, got, i, want(i))
		}
	}
}
