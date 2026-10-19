package csqlite

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// cellsTestConn builds a two-row table covering the value classes the read
// layer sees: an integer, non-empty and empty text, NULL, and a blob.
func cellsTestConn(t *testing.T) *Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cells.sqlite3")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE t(id INTEGER, name TEXT, note TEXT, data BLOB)",
		"INSERT INTO t VALUES (7, 'alice', '', x'0102')",
		"INSERT INTO t VALUES (NULL, NULL, 'x', NULL)",
		"INSERT INTO t VALUES (9, '42', NULL, NULL)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	c, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

const cellsSelect = "SELECT id,name,note,data FROM t ORDER BY rowid"

// TestStepRowMatchesLiveColumns pins the captured row against the live
// accessors column for column and row for row. The capture order in
// fastdb_step_cells is the order ColumnType/ColumnInt64/ColumnBytes use, so a
// captured cell must report exactly what the live accessor reports; any
// divergence is a semantic change for the decode that reads it.
func TestStepRowMatchesLiveColumns(t *testing.T) {
	c := cellsTestConn(t)
	live, err := c.Prepare(cellsSelect)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Finalize()
	captured, err := c.Prepare(cellsSelect)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Finalize()

	rows := 0
	for {
		okLive, err := live.Step()
		if err != nil {
			t.Fatal(err)
		}
		okCaptured, err := captured.StepRow()
		if err != nil {
			t.Fatal(err)
		}
		if okLive != okCaptured {
			t.Fatalf("row %d: live step %v, StepRow %v", rows, okLive, okCaptured)
		}
		if !okLive {
			break
		}
		for i := 0; i < 4; i++ {
			if got, want := captured.CellType(i), live.ColumnType(i); got != want {
				t.Errorf("row %d column %d: CellType %d, ColumnType %d", rows, i, got, want)
			}
			if captured.CellType(i) == TypeInteger {
				if got, want := captured.CellInt64(i), live.ColumnInt64(i); got != want {
					t.Errorf("row %d column %d: CellInt64 %d, ColumnInt64 %d", rows, i, got, want)
				}
			}
			if got, want := captured.CellBytes(i), live.ColumnBytes(i); !bytes.Equal(got, want) {
				t.Errorf("row %d column %d: CellBytes %q, ColumnBytes %q", rows, i, got, want)
			}
		}
		rows++
	}
	if rows != 3 {
		t.Fatalf("compared %d rows, want 3", rows)
	}
}

// TestStepRowBudget pins the captured-row cost: one cgo transition per row
// after the statement's C row buffer is allocated (a C allocation, not a Go
// one), and no Go allocation on the hot path. The live path costs Step plus
// one crossing per column access.
func TestStepRowBudget(t *testing.T) {
	c := cellsTestConn(t)
	st, err := c.Prepare(cellsSelect)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()

	// The first StepRow allocates the row buffer; measure the warm path.
	if _, err := st.StepRow(); err != nil {
		t.Fatal(err)
	}
	before := runtime.NumCgoCall()
	if _, err := st.StepRow(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.NumCgoCall() - before; got != 1 {
		t.Fatalf("warm StepRow crossed into C %d times, want 1", got)
	}
	if allocs := testing.AllocsPerRun(200, func() {
		if _, err := st.StepRow(); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("StepRow allocated %v objects per call, want 0", allocs)
	}
}

// TestStepRowRejectsWideStatement pins the buffer bound: a statement wider
// than the row buffer is refused rather than silently truncated.
func TestStepRowRejectsWideStatement(t *testing.T) {
	c := cellsTestConn(t)
	cols := make([]string, maxCells+1)
	for i := range cols {
		cols[i] = "1"
	}
	st, err := c.Prepare("SELECT " + strings.Join(cols, ","))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()

	ok, err := st.StepRow()
	if ok {
		t.Fatal("StepRow accepted a statement wider than its buffer")
	}
	if err != errTooWide {
		t.Fatalf("StepRow on a wide statement: %v, want errTooWide", err)
	}
}
