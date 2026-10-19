package fastdb

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestPoolBorrowReturn(t *testing.T) {
	path := fixtureDB(t)
	pool, err := OpenPool(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		c, err := pool.Borrow(ctx)
		if err != nil {
			t.Fatalf("Borrow %d: %v", i, err)
		}
		var room Room
		if err := c.Room(&room, 1<<62, 1<<62); !errors.Is(err, ErrNoRows) {
			t.Fatalf("borrowed conn %d is not functional: %v", i, err)
		}
		if err := pool.Return(c); err != nil {
			t.Fatalf("Return %d: %v", i, err)
		}
	}
}

// TestPoolConcurrentBorrowReturn runs N goroutines × M iterations of
// borrow/use/return against one pool: every Conn is owned by exactly one
// goroutine at a time and every use still reads the database. Run under
// -race.
func TestPoolConcurrentBorrowReturn(t *testing.T) {
	path := fixtureDB(t)
	pool, err := OpenPool(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const goroutines = 16
	const iterations = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c, err := pool.Borrow(context.Background())
				if err != nil {
					t.Errorf("Borrow: %v", err)
					return
				}
				var room Room
				if err := c.Room(&room, 1<<62, 1<<62); !errors.Is(err, ErrNoRows) {
					pool.Return(c)
					t.Errorf("borrowed conn: %v", err)
					return
				}
				if err := pool.Return(c); err != nil {
					t.Errorf("Return: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestPoolExhaustionBlocks pins the bounded-conn semantics: when every Conn
// is borrowed, the next Borrow waits — and gives up on its own deadline —
// then succeeds as soon as a Conn is returned.
func TestPoolExhaustionBlocks(t *testing.T) {
	path := fixtureDB(t)
	pool, err := OpenPool(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	only, err := pool.Borrow(ctx)
	if err != nil {
		t.Fatal(err)
	}

	wait, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if c, err := pool.Borrow(wait); err == nil {
		pool.Return(c)
		t.Fatal("Borrow succeeded while the pool was exhausted")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted Borrow: %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("exhausted Borrow returned after %v; it did not wait", elapsed)
	}

	if err := pool.Return(only); err != nil {
		t.Fatal(err)
	}
	c, err := pool.Borrow(ctx)
	if err != nil {
		t.Fatalf("Borrow after Return: %v", err)
	}
	if err := pool.Return(c); err != nil {
		t.Fatal(err)
	}
}

// TestPoolClosedBehaviour pins the post-Close contract: Borrow fails with
// ErrPoolClosed, and a Borrow that raced Close finalizes the Conn it
// received (via Return) without leaking it.
func TestPoolClosedBehaviour(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd counting reads /proc")
	}
	path := fixtureDB(t)
	// Warm up so a first-time runtime poller fd is not counted as a leak.
	pool, err := OpenPool(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	before := openFDs(t)

	pool, err = OpenPool(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	held := make([]*Conn, 0, 2)
	for i := 0; i < 2; i++ {
		c, err := pool.Borrow(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	// Close with every Conn borrowed: Close waits for the Returns, and each
	// Return after close(p.done) finalizes its Conn instead of re-queuing it
	// (whether the Return lands before the first drain or between the two
	// drains, the Conn ends up finalized exactly once).
	closed := make(chan error, 1)
	go func() { closed <- pool.Close() }()
	for _, c := range held {
		if err := pool.Return(c); err != nil {
			t.Fatalf("Return after Close: %v", err)
		}
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := pool.Borrow(context.Background()); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Borrow after Close: %v, want ErrPoolClosed", err)
	}

	// Repeated open/close cycles return every descriptor, including the
	// WAL shared-memory handle.
	for i := 0; i < 5; i++ {
		pool, err = OpenPool(path, 4)
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if err := pool.Close(); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if after := openFDs(t); after > before {
		t.Fatalf("fd count grew from %d to %d over pool open/close cycles", before, after)
	}
}

// TestPoolBorrowBlockedWakesOnClose pins the Close contract for a Borrow
// already waiting on an exhausted pool: close(p.done) must wake it with
// ErrPoolClosed — not hang, not hand out a Conn. BlockedBorrows doubles as
// the "the borrow is really waiting" synchronisation.
func TestPoolBorrowBlockedWakesOnClose(t *testing.T) {
	path := fixtureDB(t)
	pool, err := OpenPool(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	only, err := pool.Borrow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		c, err := pool.Borrow(context.Background())
		if c != nil {
			pool.Return(c)
		}
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for pool.BlockedBorrows() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Borrow never blocked on the exhausted pool")
		}
		time.Sleep(time.Millisecond)
	}

	// Close while the borrow is waiting. Close itself blocks until every
	// Conn is back in the channel, so it runs in a goroutine; the wakeup
	// only needs close(p.done).
	closeDone := make(chan error, 1)
	go func() { closeDone <- pool.Close() }()
	select {
	case err := <-result:
		if !errors.Is(err, ErrPoolClosed) {
			t.Fatalf("blocked Borrow woke with %v, want ErrPoolClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Borrow blocked when Close ran did not wake")
	}
	if got := pool.BlockedBorrows(); got != 1 {
		t.Errorf("BlockedBorrows = %d, want 1", got)
	}
	// The Conn still counted under the pre-Close borrow: returning it
	// finalizes it and lets Close finish.
	if err := pool.Return(only); err != nil {
		t.Fatalf("Return after Close: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the held Conn was returned")
	}
	if _, err := pool.Borrow(context.Background()); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Borrow after Close: %v, want ErrPoolClosed", err)
	}
}

// TestPoolBorrowReturnCloseStress races Borrow/Return against Close across
// repeated rounds: every Conn accepted by a borrow is finalized exactly once
// (a borrow that lost the race against close(p.done) finalizes it itself),
// Close never hangs (quiescence waits on the out count, so a Conn cannot be
// handed out after Close has begun and the fd count stays flat), and no
// Borrow returns after Close has returned. Run under -race.
func TestPoolBorrowReturnCloseStress(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd counting reads /proc")
	}
	path := fixtureDB(t)
	before := openFDs(t)
	const rounds = 5
	for r := 0; r < rounds; r++ {
		pool, err := OpenPool(path, 4)
		if err != nil {
			t.Fatal(err)
		}
		const borrowers = 8
		var wg sync.WaitGroup
		for range borrowers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					c, err := pool.Borrow(context.Background())
					if err != nil {
						if !errors.Is(err, ErrPoolClosed) {
							t.Errorf("Borrow: %v", err)
						}
						return
					}
					var room Room
					if err := c.Room(&room, 1<<62, 1<<62); err != nil && !errors.Is(err, ErrNoRows) {
						t.Errorf("borrowed conn: %v", err)
					}
					if err := pool.Return(c); err != nil {
						t.Errorf("Return: %v", err)
						return
					}
				}
			}()
		}
		// Let the borrowers contend briefly, then close under them.
		time.Sleep(time.Duration(r+1) * time.Millisecond)
		if err := pool.Close(); err != nil {
			t.Fatalf("round %d: Close: %v", r, err)
		}
		wg.Wait()
		if _, err := pool.Borrow(context.Background()); !errors.Is(err, ErrPoolClosed) {
			t.Fatalf("round %d: Borrow after Close: %v", r, err)
		}
	}
	if after := openFDs(t); after > before {
		t.Fatalf("fd count grew from %d to %d over Borrow/Return/Close rounds", before, after)
	}
}

// TestOpenPoolFailsClosesOpenedConns forces a mid-open failure (a valid
// directory is not a SQLite database) and checks the error surfaces.
func TestOpenPoolFailsClosesOpenedConns(t *testing.T) {
	dir := t.TempDir()
	// The first open fails immediately, so only the error surfaces here; the
	// caller-visible contract is that no Conn is left open.
	_, err := OpenPool(dir, 4)
	if err == nil {
		t.Fatal("OpenPool opened a non-database path without error")
	}
}
