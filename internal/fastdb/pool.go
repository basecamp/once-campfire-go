package fastdb

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
// drains the free Conns both before and after waiting for every Conn to be
// back in the channel. The pool must not be used after Close.
type Pool struct {
	conns chan *Conn
	done  chan struct{}
	once  sync.Once
	// out counts Conns outside the channel: Borrow increments it before its
	// receive can remove a Conn, and exactly one path decrements it — the
	// successful Return, acquire's self-finalize branch, or Borrow's error
	// exits (which never removed a Conn). Invariant: every Conn removed from
	// the channel is counted before it is either handed out or finalized,
	// and out == 0 means every Conn is back in the channel with no Return
	// or borrow still in flight, so Close's final drain is the last word on
	// the channel. An atomic is required rather than a WaitGroup: the pool's
	// counter oscillates through zero during normal borrow/return traffic,
	// and a WaitGroup's "positive delta at counter zero must happen before
	// Wait" rule (checked by the race detector through the semaphore sync)
	// would be violated by any borrow racing Close's Wait.
	out atomic.Int64
	// allReturned is closed by the last out decrement once Close has begun;
	// Close waits on it so its closing drain runs only after no borrow or
	// Return is in flight.
	allReturned chan struct{}
	allOnce     sync.Once
	// blocked counts Borrow calls that found no free Conn and had to wait.
	// Diagnostic saturation counter; monotonic, zero until the first wait.
	blocked atomic.Uint64
}

// OpenPool opens size read-only Conns on path. Size below one opens one Conn.
// When an open fails partway, the Conns already opened are closed before the
// error is returned.
func OpenPool(path string, size int) (*Pool, error) {
	if size < 1 {
		size = 1
	}
	p := &Pool{conns: make(chan *Conn, size), done: make(chan struct{}), allReturned: make(chan struct{})}
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
	// Count the borrow before the receive can remove a Conn (see the out
	// field comment); every exit below releases it exactly once.
	p.out.Add(1)
	select {
	case c := <-p.conns:
		return p.acquire(c)
	case <-p.done:
		p.release()
		return nil, ErrPoolClosed
	default:
	}
	// No Conn was free and the pool was still open: this borrow waits, and
	// counts as a blocked borrow.
	p.blocked.Add(1)
	select {
	case c := <-p.conns:
		return p.acquire(c)
	case <-p.done:
		p.release()
		return nil, ErrPoolClosed
	case <-ctx.Done():
		p.release()
		return nil, ctx.Err()
	}
}

// acquire claims a Conn just removed from the channel. If Close began
// between the receive and the check, the drained channel will never see the
// Conn again, so it is finalized here instead of being handed out.
func (p *Pool) acquire(c *Conn) (*Conn, error) {
	select {
	case <-p.done:
		c.Close()
		p.release()
		return nil, ErrPoolClosed
	default:
	}
	return c, nil
}

// release uncounts a borrow taken in Borrow. The last release once Close has
// begun closes allReturned, unblocking Close's wait.
func (p *Pool) release() {
	if p.out.Add(-1) == 0 {
		select {
		case <-p.done:
			p.allOnce.Do(func() { close(p.allReturned) })
		default:
		}
	}
}

// BlockedBorrows reports how many Borrow calls have had to wait for a free
// Conn since the pool opened. Diagnostic saturation counter; monotonic.
func (p *Pool) BlockedBorrows() uint64 { return p.blocked.Load() }

// Return hands a borrowed Conn back to the pool. After Close has begun it
// finalizes the Conn instead. The send (when it happens) precedes the
// release, so Close's wait cannot observe quiescence while a Conn is still
// on its way back into the channel.
func (p *Pool) Return(c *Conn) error {
	select {
	case <-p.done:
		err := c.Close()
		p.release()
		return err
	default:
	}
	// The channel always has room for a Conn taken from it, so this send
	// never blocks.
	p.conns <- c
	p.release()
	return nil
}

// Close shuts the pool down. Idempotent: subsequent calls return without
// touching the Conns (Borrow and Return still honour the closed state).
func (p *Pool) Close() error {
	var first error
	p.once.Do(func() {
		close(p.done)
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
		// Wait until every Conn is back in the channel (or was finalized by
		// the borrow that held it); the pool can be already quiescent when
		// Close starts, so also fire the release signal here.
		if p.out.Load() == 0 {
			p.allOnce.Do(func() { close(p.allReturned) })
		}
		<-p.allReturned
		// A Return that saw the pool open and re-queued its Conn just as the
		// first drain finished was released by then, so its Conn is in the
		// channel now — picked up here. Nothing can appear after this drain:
		// out == 0 proves no borrow or Return is in flight.
		drain()
	})
	return first
}
