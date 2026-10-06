package fastdb

import (
	"context"
	"errors"
	"sync"
)

// ErrPoolClosed is returned by Borrow (and honoured by Return) after the pool
// has been closed.
var ErrPoolClosed = errors.New("fastdb: pool closed")

// Pool is a bounded set of read-only Conns for the web read paths. A Conn is
// not safe for concurrent use (SQLITE_OPEN_NOMUTEX), so each request borrows
// one Conn for its reads and returns it; the pool size bounds the number of
// requests reading at the same time, which is the same ceiling the legacy
// read pool applies (max open = application CPUs).
//
// Close makes every Conn finalize its statements and close its file
// descriptor exactly once, even when a borrow or return races it: Borrow
// refuses (and finalizes) a Conn received after Close has started, Return
// finalizes its Conn instead of re-queuing it once Close has begun, and Close
// drains the free Conns both before and after waiting for outstanding
// borrows. The pool must not be used after Close.
type Pool struct {
	conns chan *Conn
	done  chan struct{}
	once  sync.Once
	// wg counts borrowed Conns: Add in Borrow (before handing over), Done in
	// Return (always, whichever branch closes the Conn).
	wg sync.WaitGroup
}

// OpenPool opens size read-only Conns on path. Size below one opens one Conn.
// When an open fails partway, the Conns already opened are closed before the
// error is returned.
func OpenPool(path string, size int) (*Pool, error) {
	if size < 1 {
		size = 1
	}
	p := &Pool{conns: make(chan *Conn, size), done: make(chan struct{})}
	for i := 0; i < size; i++ {
		c, err := OpenReadOnly(path, 0)
		if err != nil {
			p.Close()
			return nil, err
		}
		p.conns <- c
	}
	return p, nil
}

// Borrow returns a Conn, blocking until one is free. It fails with
// context.Canceled/DeadlineExceeded when ctx ends first, and with ErrPoolClosed
// when the pool has been closed. The returned Conn is owned by exactly one
// goroutine until it is handed back with Return.
func (p *Pool) Borrow(ctx context.Context) (*Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case c := <-p.conns:
		select {
		case <-p.done:
			// Close began between this receive and the check; the drained
			// channel will never see the Conn again, so finalize it here.
			c.Close()
			return nil, ErrPoolClosed
		default:
		}
		p.wg.Add(1)
		return c, nil
	case <-p.done:
		return nil, ErrPoolClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Return hands a borrowed Conn back to the pool. After Close has begun it
// finalizes the Conn instead (Close's WaitGroup covers the borrow, so the
// finalize never races the drain).
func (p *Pool) Return(c *Conn) error {
	defer p.wg.Done()
	select {
	case <-p.done:
		return c.Close()
	default:
	}
	// The channel always has room for a Conn taken from it, so this send
	// never blocks.
	p.conns <- c
	return nil
}

// Close shuts the pool down. Idempotent: subsequent calls return without
// touching the Conns (Borrow and Return still honour the closed state).
func (p *Pool) Close() error {
	p.once.Do(func() { close(p.done) })
	var first error
	drain := func() {
		for {
			select {
			case c := <-p.conns:
				if err := c.Close(); err != nil && first == nil {
					first = err
				}
			default:
				return
			}
		}
	}
	drain()
	// Wait for borrowed Conns: each Return since close(p.done) finalizes its
	// Conn, so nothing waits here depends on the drain.
	p.wg.Wait()
	// A Return that saw the pool open and re-queued its Conn just as the
	// first drain finished is picked up now; after wg.Wait no Return is in
	// flight, so nothing can appear after this drain.
	drain()
	return first
}
