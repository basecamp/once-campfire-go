package database

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func openTestDB(t *testing.T, readers int) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	d, err := Open(path, readers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func TestPanickingWriteRollsBackAndLeavesTheWriterUsable(t *testing.T) {
	d, _ := openTestDB(t, 1)
	ctx := context.Background()
	if _, err := d.Write.Exec("CREATE TABLE things (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the write's panic didn't reach its caller")
			}
		}()
		d.Transaction(ctx, func(tx *Tx) error {
			if _, err := tx.Exec("INSERT INTO things VALUES (1)"); err != nil {
				return err
			}
			panic("a bug in a write")
		})
	}()
	if err := d.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.Exec("INSERT INTO things VALUES (2)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var ids string
	if err := d.Read.QueryRow("SELECT group_concat(id) FROM things").Scan(&ids); err != nil || ids != "2" {
		t.Fatalf("ids %q, %v", ids, err)
	}
}

func TestAfterCommitRunsInOrderOnlyAfterACommit(t *testing.T) {
	d, _ := openTestDB(t, 1)
	ctx := context.Background()
	if _, err := d.Write.Exec("CREATE TABLE things (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	var ran []string
	err := d.Transaction(ctx, func(tx *Tx) error {
		tx.AfterCommit(func(tx *Tx) error {
			ran = append(ran, "first")
			_, err := tx.Exec("INSERT INTO things VALUES (2)")
			return err
		})
		tx.AfterCommit(func(tx *Tx) error { ran = append(ran, "second"); return nil })
		_, err := tx.Exec("INSERT INTO things VALUES (1)")
		return err
	})
	if err != nil || strings.Join(ran, ",") != "first,second" {
		t.Fatalf("ran %v, %v", ran, err)
	}
	failed := d.Transaction(ctx, func(tx *Tx) error {
		tx.AfterCommit(func(tx *Tx) error { ran = append(ran, "rolled back"); return nil })
		return ErrForbidden
	})
	if failed != ErrForbidden || len(ran) != 2 {
		t.Fatalf("after a rollback: ran %v, %v", ran, failed)
	}
	var count int
	if err = d.Read.QueryRow("SELECT count(*) FROM things").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestReadsWaitForAConnectionInUse(t *testing.T) {
	d, _ := openTestDB(t, 1)
	held, err := d.Read.QueryContext(context.Background(), "SELECT 1 UNION ALL SELECT 2")
	if err != nil {
		t.Fatal(err)
	}
	if !held.Next() {
		t.Fatal(held.Err())
	}
	done := make(chan int, 1)
	go func() {
		var n int
		d.Read.QueryRow("SELECT 3").Scan(&n)
		done <- n
	}()
	select {
	case <-done:
		t.Fatal("a read ran on the connection in use")
	case <-time.After(50 * time.Millisecond):
	}
	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := d.Read.QueryContext(timeout, "SELECT 4"); err == nil {
		t.Fatal("a read past its deadline ran")
	}
	held.Close()
	if n := <-done; n != 3 {
		t.Fatal(n)
	}
}

// The bundled SQLite must include 3.51.3's fix for the WAL-reset bug, which a checkpointer
// next to the writer could otherwise hit (https://sqlite.org/wal.html#walresetbug).
func TestBundledSQLiteHasTheWALResetFix(t *testing.T) {
	d, _ := openTestDB(t, 1)
	var version string
	if err := d.Read.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(version, ".")
	number := 0
	for _, part := range parts {
		n, _ := strconv.Atoi(part)
		number = number*1000 + n
	}
	if number < 3_051_003 {
		t.Fatalf("SQLite %s", version)
	}
}

func fill(t *testing.T, d *DB, rows int) {
	t.Helper()
	err := d.Transaction(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) INSERT INTO filler SELECT randomblob(3900) FROM n", rows)
		return err
	})
	if err != nil {
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

// Commits never checkpoint on the writer: the WAL reaching the auto-checkpoint threshold wakes
// the checkpointer, which copies it into the database file on its own.
func TestTheCheckpointerCopiesTheWALIntoTheDatabase(t *testing.T) {
	d, path := openTestDB(t, 1)
	if _, err := d.Write.Exec("CREATE TABLE filler (data BLOB)"); err != nil {
		t.Fatal(err)
	}
	before := fileSize(t, path)
	for range 6 {
		fill(t, d, 200) // ~1,200 pages of 4 KiB, over a few commits
	}
	deadline := time.Now().Add(10 * time.Second)
	for fileSize(t, path) < before+1000*4096 {
		if time.Now().After(deadline) {
			t.Fatal("the WAL was never checkpointed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Writes that never pause still get the WAL restarted, at walLimitPages.
func TestTheWALStaysBoundedUnderSustainedWrites(t *testing.T) {
	d, path := openTestDB(t, 1)
	if _, err := d.Write.Exec("CREATE TABLE filler (data BLOB)"); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		fill(t, d, 500) // ~25,000 pages, 500 per commit
	}
	if wal := fileSize(t, path+"-wal"); wal >= (walLimitPages+1000)*4200 {
		t.Fatalf("WAL of %d bytes", wal)
	}
}
