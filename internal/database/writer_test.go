package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Ports of the writer and reader tests in reference/crates/db/src/database.rs. A
// panicking write raises its panic in the caller here, where the reference returns
// WriterGone; either way it rolls back and the writer carries on.

func thingsDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err = d.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE things (id INTEGER)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func insertThing(id int) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO things VALUES (?)", id)
		return err
	}
}

func things(t *testing.T, d *DB) string {
	t.Helper()
	var ids sql.NullString
	if err := d.Read.QueryRowContext(context.Background(), "SELECT group_concat(id) FROM things").Scan(&ids); err != nil {
		t.Fatal(err)
	}
	return ids.String
}

func panicked(fn func()) (value any) {
	defer func() { value = recover() }()
	fn()
	return nil
}

// a_panicking_write_rolls_back and a_panicking_write_leaves_the_writer_usable.
func TestAPanickingWriteRollsBackAndLeavesTheWriterUsable(t *testing.T) {
	d := thingsDB(t)
	ctx := context.Background()
	if value := panicked(func() {
		d.Transaction(ctx, func(tx *sql.Tx) error {
			if err := insertThing(1)(tx); err != nil {
				return err
			}
			panic("a bug in a write")
		})
	}); value != "a bug in a write" {
		t.Fatalf("panic %v", value)
	}
	if err := d.Transaction(ctx, insertThing(2)); err != nil {
		t.Fatal(err)
	}
	if ids := things(t, d); ids != "2" {
		t.Fatalf("things %q", ids)
	}
}

func TestAFailingWriteRollsBack(t *testing.T) {
	d := thingsDB(t)
	failure := errors.New("failure")
	if err := d.Transaction(context.Background(), func(tx *sql.Tx) error {
		insertThing(1)(tx)
		return failure
	}); err != failure {
		t.Fatalf("error %v", err)
	}
	if ids := things(t, d); ids != "" {
		t.Fatalf("things %q", ids)
	}
}

// Writes from many callers each commit once, on the one writer.
func TestConcurrentWritesAllCommit(t *testing.T) {
	d := thingsDB(t)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			if err := d.Transaction(context.Background(), insertThing(i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var count int
	if err := d.Read.QueryRowContext(context.Background(), "SELECT count(DISTINCT id) FROM things").Scan(&count); err != nil || count != 64 {
		t.Fatalf("things %d: %v", count, err)
	}
}

func TestAWriteAfterCloseFails(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.Transaction(context.Background(), func(*sql.Tx) error { return nil }); err != ErrClosed {
		t.Fatalf("after close: %v", err)
	}
}

// a_read_given_up_while_it_waits_still_runs: a read waiting for the only reader
// connection runs once it is free, even though its caller stopped waiting.
func TestAReadGivenUpWhileItWaitsStillRuns(t *testing.T) {
	d := thingsDB(t)
	if err := d.Transaction(context.Background(), insertThing(7)); err != nil {
		t.Fatal(err)
	}
	holder, err := d.Read.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		var id int
		err := d.Read.QueryRowContext(ctx, "SELECT id FROM things").Scan(&id)
		if err == nil && id != 7 {
			err = errors.New("wrong row")
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("the reader is busy, but the read finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	holder.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the abandoned read failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the abandoned read never ran")
	}
}
