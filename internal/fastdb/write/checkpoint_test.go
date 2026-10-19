package write

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckpointRestartReportsRefusedCheckpoint is the regression for the
// refused-restart HIGH: SQLite reports an incomplete checkpoint in the
// pragma's result row (busy, log, checkpointed), not as an execution error, so
// a RESTART that a pinned reader refused must come back Completed=false rather
// than as success. It also pins the bounded wait: the attempt runs with
// CheckpointBusyMS rather than the connection's 5000 ms busy timeout, so a
// pinned reader cannot stall the caller for seconds.
func TestCheckpointRestartReportsRefusedCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refused.sqlite3")
	conn, err := OpenWriter(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()
	insert := func(rows int) {
		t.Helper()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY AUTOINCREMENT, a TEXT)"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < rows; i++ {
			if err := tx.Exec("INSERT INTO t(a) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	insert(80)

	// A read transaction on its own connection pins a WAL read slot; SQLite's
	// RESTART needs every slot free, so it is refused.
	reader, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	reader.SetMaxOpenConns(1)
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Write past the reader's snapshot so the restart has frames to move.
	insert(80)

	start := time.Now()
	result, err := conn.CheckpointRestart()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("CheckpointRestart: %v", err)
	}
	if result.Completed {
		t.Fatalf("CheckpointRestart reported a completed restart with a reader pinned: %+v", result)
	}
	if result.WALFrames <= 0 {
		t.Fatalf("refused restart did not report the WAL frame count: %+v", result)
	}
	if bound := time.Duration(CheckpointBusyMS+1000) * time.Millisecond; elapsed > bound {
		t.Fatalf("refused restart took %v, want under %v (bounded busy wait)", elapsed, bound)
	}
	// The writer connection's normal busy timeout is restored after the
	// bounded attempt.
	if got := pragmaInt64(t, conn, "PRAGMA busy_timeout"); got != BusyTimeoutMS {
		t.Fatalf("busy_timeout after the attempt = %d, want %d", got, BusyTimeoutMS)
	}

	// Release the reader: the same attempt now completes and resets the WAL.
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	result, err = conn.CheckpointRestart()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed {
		t.Fatalf("CheckpointRestart still refused after the reader released: %+v", result)
	}
	if result.Backfilled != result.WALFrames {
		t.Fatalf("completed restart backfilled %d of %d frames", result.Backfilled, result.WALFrames)
	}
}

// pragmaInt64 runs one scalar pragma on the connection (the test holds it).
func pragmaInt64(t *testing.T, conn *WriteConn, query string) int64 {
	t.Helper()
	st, err := conn.db.Prepare(query)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	row, err := st.Step()
	if err != nil || !row {
		t.Fatalf("%s: row=%v err=%v", query, row, err)
	}
	value := st.ColumnInt64(0)
	if err := st.Reset(); err != nil {
		t.Fatal(err)
	}
	return value
}
