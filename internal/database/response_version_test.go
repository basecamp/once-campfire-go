package database

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestResponseVersionWaitingReadIsCancellable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := testDB(t)
		d.version.admission <- struct{}{}
		defer func() { <-d.version.admission }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := d.ResponseVersion(ctx); result <- err }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		default:
			t.Fatal("cancelled query waited for admission")
		}
	})
}

func TestVersionObserverCancellationDoesNotCancelSharedReader(t *testing.T) {
	d := testDB(t)
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		d.version.connection.Raw(func(any) error { close(entered); <-release; return nil })
	}()
	<-entered
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := d.version.request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	live := make(chan error, 1)
	go func() { _, err := d.ResponseVersion(context.Background()); live <- err }()
	unblock()
	select {
	case err := <-live:
		if err != nil {
			t.Fatal("another caller inherited cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live observation did not finish")
	}
	if result := <-first; result.err != nil {
		t.Fatal("waiter cancellation stopped the shared query", result.err)
	}
}

func TestVersionObserverCloseJoinsResourceOwner(t *testing.T) {
	d := testDB(t)
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		d.version.connection.Raw(func(any) error { close(entered); <-release; return nil })
	}()
	<-entered
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	observed := make(chan error, 1)
	go func() { _, err := d.ResponseVersion(context.Background()); observed <- err }()
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- d.version.Close() }()
	}
	select {
	case <-closed:
		t.Fatal("close returned while the driver connection remained owned")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("close did not join")
		}
	}
	if err := <-observed; !errors.Is(err, context.Canceled) {
		t.Fatal("waiting caller did not observe shutdown", err)
	}
	if _, err := d.ResponseVersion(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("closed observer admitted a new request", err)
	}
}

func TestResponseVersionObservesForeignCommitsDuringConcurrentReads(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if _, err := d.Setup(ctx, "User", "user@test", "digest"); err != nil {
		t.Fatal(err)
	}
	var index int
	var name, path string
	if err := d.Read.QueryRow("PRAGMA database_list").Scan(&index, &name, &path); err != nil {
		t.Fatal(err)
	}
	foreign, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	stop := make(chan struct{})
	failures := make(chan error, 8)
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := d.ResponseVersion(ctx); err != nil {
					failures <- err
					return
				}
			}
		})
	}
	defer func() { close(stop); readers.Wait() }()
	for i := range 100 {
		before, err := d.ResponseVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := foreign.ExecContext(ctx, "UPDATE accounts SET name=?", strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		after, err := d.ResponseVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after <= before {
			t.Fatalf("commit %d: generation %d did not advance from %d", i, after, before)
		}
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.ResponseVersion(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled generation query: %v", err)
	}
}
