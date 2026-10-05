package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Ports of the WAL tests in reference/crates/db/src/database.rs.

func fillerDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	d, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err = d.Write.Exec("CREATE TABLE filler (data BLOB)"); err != nil {
		t.Fatal(err)
	}
	return d, path
}

// rows of ~one 4 KiB page each, in one commit.
func fill(t *testing.T, d *DB, rows int) {
	t.Helper()
	if _, err := d.Write.Exec("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) INSERT INTO filler SELECT randomblob(3900) FROM n", rows); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// the_checkpointer_copies_the_wal_into_the_database: commits never checkpoint on the
// writer; the WAL reaching the auto-checkpoint threshold wakes the checkpointer, which
// copies it into the database file on its own.
func TestTheCheckpointerCopiesTheWALIntoTheDatabase(t *testing.T) {
	d, path := fillerDB(t)
	before := fileSize(t, path)
	// ~1,200 pages of 4 KiB, over a few commits.
	for range 6 {
		fill(t, d, 200)
	}
	deadline := time.Now().Add(10 * time.Second)
	for fileSize(t, path) < before+1000*4096 {
		if time.Now().After(deadline) {
			t.Fatal("the WAL was never checkpointed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.checkpoints.ran.Load() == 0 {
		t.Fatal("the database grew without the checkpointer")
	}
}

// the_wal_stays_bounded_under_sustained_writes: writes that never pause still get the
// WAL restarted, at walLimitPages.
func TestTheWALStaysBoundedUnderSustainedWrites(t *testing.T) {
	d, path := fillerDB(t)
	// ~25,000 pages, 500 per commit.
	for range 50 {
		fill(t, d, 500)
	}
	if wal := fileSize(t, path+"-wal"); wal >= (walLimitPages+1000)*4200 {
		t.Fatalf("WAL of %d bytes", wal)
	}
}

// the_bundled_sqlite_has_the_wal_reset_fix: before 3.51.3, a checkpoint that starts just
// as another connection's commit restarts the WAL can leave that commit out of the
// database (https://sqlite.org/wal.html#walresetbug). The checkpointer and the writer are
// two such connections.
func TestTheBundledSQLiteHasTheWALResetFix(t *testing.T) {
	d := testDB(t)
	var raw string
	if err := d.Read.QueryRowContext(context.Background(), "SELECT sqlite_version()").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var major, minor, patch int
	if _, err := fmt.Sscanf(raw, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatal(err)
	}
	if major*1_000_000+minor*1_000+patch < 3_051_003 {
		t.Fatalf("SQLite %s", raw)
	}
}
