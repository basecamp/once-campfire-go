package sqlite

import (
	"bytes"
	"testing"
)

// Rows read by campfire_step answer the accessors as SQLite's own column calls do, including
// when a value must be converted (which still goes to SQLite) and after the row is gone.
func TestRowValues(t *testing.T) {
	conn, err := OpenConn("file::memory:", SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE|SQLITE_OPEN_URI)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	stmt, err := conn.Prepare("SELECT 42, 2.5, 'héllo', x'00ff', NULL, x'', '', '17', 3")
	if err != nil {
		t.Fatal(err)
	}
	if got := stmt.ColumnCount(); got != 9 {
		t.Fatalf("ColumnCount before Step = %d, want 9", got)
	}
	if row, err := stmt.Step(); err != nil || !row {
		t.Fatalf("Step = %v, %v", row, err)
	}
	types := []ColumnType{SQLITE_INTEGER, SQLITE_FLOAT, SQLITE_TEXT, SQLITE_BLOB, SQLITE_NULL, SQLITE_BLOB, SQLITE_TEXT, SQLITE_TEXT, SQLITE_INTEGER}
	for col, want := range types {
		if got := stmt.ColumnType(col); got != want {
			t.Errorf("ColumnType(%d) = %v, want %v", col, got, want)
		}
	}
	if got := stmt.ColumnInt64(0); got != 42 {
		t.Errorf("ColumnInt64(0) = %d", got)
	}
	if got := stmt.ColumnFloat(1); got != 2.5 {
		t.Errorf("ColumnFloat(1) = %v", got)
	}
	if got := stmt.ColumnText(2); got != "héllo" {
		t.Errorf("ColumnText(2) = %q", got)
	}
	if got := stmt.ColumnLen(2); got != len("héllo") {
		t.Errorf("ColumnLen(2) = %d", got)
	}
	blob := make([]byte, stmt.ColumnLen(3))
	if n := stmt.ColumnBytes(3, blob); n != 2 || !bytes.Equal(blob, []byte{0, 0xff}) {
		t.Errorf("ColumnBytes(3) = %d %v", n, blob)
	}
	if stmt.ColumnInt64(4) != 0 || stmt.ColumnText(4) != "" || stmt.ColumnLen(4) != 0 || stmt.ColumnFloat(4) != 0 || stmt.ColumnReader(4).Len() != 0 {
		t.Errorf("NULL column read as non-zero")
	}
	if stmt.ColumnLen(5) != 0 || stmt.ColumnReader(5).Len() != 0 || stmt.ColumnText(6) != "" {
		t.Errorf("empty blob or text read as non-empty")
	}
	// Conversions SQLite makes: text to integer, integer to text, integer to float.
	if got := stmt.ColumnInt64(7); got != 17 {
		t.Errorf("ColumnInt64 of '17' = %d", got)
	}
	if got := stmt.ColumnText(8); got != "3" {
		t.Errorf("ColumnText of 3 = %q", got)
	}
	if got := stmt.ColumnFloat(0); got != 42 {
		t.Errorf("ColumnFloat of 42 = %v", got)
	}
	if got := stmt.ColumnText(1); got != "2.5" {
		t.Errorf("ColumnText of 2.5 = %q", got)
	}
	if row, err := stmt.Step(); err != nil || row {
		t.Fatalf("second Step = %v, %v", row, err)
	}
	// No current row: the accessors fall through to SQLite, which answers NULL.
	if got := stmt.ColumnType(0); got != SQLITE_NULL {
		t.Errorf("ColumnType after the last row = %v", got)
	}
	if err := stmt.Reset(); err != nil {
		t.Fatal(err)
	}
	if row, err := stmt.Step(); err != nil || !row || stmt.ColumnInt64(0) != 42 {
		t.Fatalf("Step after Reset = %v, %v, %d", row, err, stmt.ColumnInt64(0))
	}
	stmt.Reset()
	if got := stmt.ColumnType(0); got != SQLITE_NULL {
		t.Errorf("ColumnType after Reset = %v", got)
	}
}

// Each step reads its own row, and a statement with parameters keeps its parameter count.
func TestRowValuesAcrossSteps(t *testing.T) {
	conn, err := OpenConn("file::memory:", SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE|SQLITE_OPEN_URI)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmt, err := conn.Prepare("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) SELECT i, 'row ' || i, CASE WHEN i % 2 THEN NULL ELSE i END FROM n")
	if err != nil {
		t.Fatal(err)
	}
	if got := stmt.BindParamCount(); got != 1 {
		t.Fatalf("BindParamCount = %d", got)
	}
	stmt.BindInt64(1, 1000)
	var i int64
	for {
		row, err := stmt.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !row {
			break
		}
		i++
		if got := stmt.ColumnInt64(0); got != i {
			t.Fatalf("row %d: ColumnInt64 = %d", i, got)
		}
		if got, want := stmt.ColumnText(1), "row "+stmt.ColumnText(0); got != want {
			t.Fatalf("row %d: ColumnText = %q, want %q", i, got, want)
		}
		if odd := i%2 == 1; odd != (stmt.ColumnType(2) == SQLITE_NULL) {
			t.Fatalf("row %d: ColumnType(2) = %v", i, stmt.ColumnType(2))
		}
	}
	if i != 1000 {
		t.Fatalf("read %d rows", i)
	}
}
