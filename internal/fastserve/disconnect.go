package fastserve

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/sebishogun/simdhttp/http1"
)

// disconnectReader owns the socket only between body EOF and handler return.
// Like net/http's connReader it reads one byte, ignores an intentional abort's
// timeout, and replays a successful read before the next ordinary socket read.
// Buffered pipeline bytes are untouched: only the socket can report a peer EOF.
// One worker and three channels are allocated per connection, not per request.
// stop joins each read; close also joins the parked worker before connDone.
// All fields except aborting are owned by the request goroutine, or transferred
// to/from the worker by work/done. The worker never touches br or pr.pending.
type disconnectReader struct {
	c        *conn
	cancel   context.CancelFunc
	work     chan context.CancelFunc
	done     chan struct{}
	exited   chan struct{}
	active   bool
	aborting atomic.Bool
	byte     [1]byte
	hasByte  bool
}

func (r *disconnectReader) start() {
	if r.cancel == nil || r.active || r.hasByte {
		return
	}
	if r.work == nil {
		r.work = make(chan context.CancelFunc)
		r.done = make(chan struct{}, 1)
		r.exited = make(chan struct{})
		go r.run()
	}
	// The body deadline no longer applies after EOF (net/http does the same).
	r.c.setReadDeadline(time.Time{})
	r.aborting.Store(false)
	r.active = true
	r.work <- r.cancel
}

func (r *disconnectReader) run() {
	defer close(r.exited)
	for cancel := range r.work {
		n, err := r.c.rwc.Read(r.byte[:])
		r.hasByte = n > 0
		if err != nil {
			ne, timeout := err.(net.Error)
			if !(r.aborting.Load() && timeout && ne.Timeout()) {
				cancel()
			}
		}
		r.done <- struct{}{}
	}
}

func (r *disconnectReader) stop() {
	if !r.active {
		return
	}
	select {
	case <-r.done:
	default:
		r.aborting.Store(true)
		// Bypass no deadline state: the mirror must record this interrupt too.
		r.c.setReadDeadline(time.Unix(1, 0))
		<-r.done
		r.c.setReadDeadline(time.Time{})
	}
	r.active = false
}

func (r *disconnectReader) close() {
	r.stop()
	if r.work != nil {
		close(r.work)
		<-r.exited
	}
}

// Keep framing state and EOF notification in one allocation, replacing the
// existing BodyReader allocation. A fixed body is complete even if the caller
// reads exactly Content-Length bytes without making a final EOF read.
type observedBody struct {
	http1.BodyReader
	observer  *disconnectReader
	remaining int64
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.BodyReader.Read(p)
	if b.remaining > 0 {
		b.remaining -= int64(n)
	}
	if err == io.EOF || (err == nil && b.remaining == 0) {
		b.observer.start()
	}
	return n, err
}

func (b *observedBody) Close() error {
	err := b.BodyReader.Close()
	if b.BodyReader.Drained() {
		b.observer.start()
	}
	return err
}
