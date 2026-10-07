//go:build !js

package websocket

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// PreparedMessage is an immutable server broadcast. Its compressed form is
// computed once and shared by connections without context takeover.
// This is the only extension to coder/websocket v1.8.15 in this local fork.
type PreparedMessage struct {
	typ        MessageType
	data       []byte
	once       sync.Once
	compressed []byte
	err        error
}

func NewPreparedMessage(typ MessageType, data []byte) *PreparedMessage {
	return &PreparedMessage{typ: typ, data: bytes.Clone(data)}
}
func (p *PreparedMessage) deflate() ([]byte, error) {
	p.once.Do(func() {
		var out bytes.Buffer
		writer := getFlateWriter(&out)
		defer putFlateWriter(writer)
		if _, p.err = writer.Write(p.data); p.err != nil {
			return
		}
		if p.err = writer.Flush(); p.err != nil {
			return
		}
		// RFC 7692 omits the sync-flush trailer; each receiver restores it.
		p.compressed = out.Bytes()[:out.Len()-4]
	})
	return p.compressed, p.err
}

// Data returns the message payload bytes. The returned slice must not be
// mutated; the observable payload never changes after NewPreparedMessage.
func (p *PreparedMessage) Data() []byte {
	return p.data
}

// WritePrepared writes a shared server frame. Connections using context takeover
// and client connections retain the ordinary writer and its masking/history.
func (c *Conn) WritePrepared(ctx context.Context, p *PreparedMessage) error {
	if c.client || c.flate() && !c.copts.serverNoContextTakeover {
		return c.Write(ctx, p.typ, p.data)
	}
	if err := c.msgWriter.reset(ctx, p.typ); err != nil {
		return err
	}
	defer c.msgWriter.mu.unlock()
	data := p.data
	compressed := c.flate() && len(data) >= c.flateThreshold
	if compressed {
		var err error
		data, err = p.deflate()
		if err != nil {
			return err
		}
	}
	_, err := c.writeFrame(ctx, true, compressed, c.msgWriter.opcode, data)
	return err
}

// WritePreparedBatch writes every prepared message as consecutive frames and
// coalesces the whole batch into a single network write (a vectored writev
// when the underlying connection supports it) instead of one write per
// frame. Frame bytes are identical to a sequence of WritePrepared calls;
// header and payload buffers are reused across wakes, so a batch allocates
// nothing beyond the first.
//
// msgs must contain at most batchMaxFrames messages; the caller is expected
// to bound its batches (the hub drains up to that many queued frames per
// wake). Connections using context takeover and client connections fall
// back to per-message WritePrepared.
func (c *Conn) WritePreparedBatch(ctx context.Context, msgs []*PreparedMessage) (err error) {
	if len(msgs) == 0 {
		return nil
	}
	if c.client || c.flate() && !c.copts.serverNoContextTakeover {
		for _, m := range msgs {
			if err := c.WritePrepared(ctx, m); err != nil {
				return err
			}
		}
		return nil
	}
	return c.writePreparedBatchCore(ctx, time.Time{}, false, msgs)
}

// WritePreparedBatchDeadline writes a batch exactly like WritePreparedBatch
// but bounds the write with an absolute socket write deadline instead of a
// context. The deadline is installed before the vectored write and cleared
// after it, so a stalled socket is still reaped at the deadline while a
// flowing socket pays two deadline stores per wake and no timers or
// allocations (ENGINE-54: the hub's per-wake context armed and stopped two
// runtime timers and allocated per wake, which at 1000 clients was the
// dominant socket-level cost). The connection is not closed on expiry; the
// write returns an i/o timeout error and the caller is expected to tear the
// connection down, as the hub does.
//
// WritePreparedBatchDeadlineRetained is the variant that keeps the deadline
// armed after the write; the hub's ENGINE-61 write path uses that one.
func (c *Conn) WritePreparedBatchDeadline(deadline time.Time, msgs []*PreparedMessage) (err error) {
	if len(msgs) == 0 {
		return nil
	}
	if c.client || c.flate() && !c.copts.serverNoContextTakeover {
		for _, m := range msgs {
			if err := c.WritePrepared(context.Background(), m); err != nil {
				return err
			}
		}
		return nil
	}
	return c.writePreparedBatchCore(nil, deadline, false, msgs)
}

// WritePreparedBatchDeadlineRetained writes a batch exactly like
// WritePreparedBatchDeadline but leaves the socket write deadline armed
// after the write returns: the deadline is installed before the vectored
// write and never cleared. A later write on the same connection that arms
// no deadline of its own — the hub's ENGINE-61 unarmed branch — therefore
// still runs under the remaining budget and a stalled socket is reaped at
// the original deadline (ENGINE-61: the hub arms at most once per
// cableDeadlineRearm and relies on the deadline persisting between arms;
// WritePreparedBatchDeadline's clear-after-write left those batches
// unbounded). The deadline is absolute: a write that starts after it has
// passed fails immediately with an i/o timeout, so callers must re-arm
// (as the hub does) rather than rely on one retained deadline forever.
// A fully-flowing socket pays one deadline store per re-arm and nothing
// between arms: no timers and no allocations on the unarmed wake.
// Callers that need the deadline cleared after the write (a connection
// shared with deadline-less readers or reused for later unbounded writes)
// must use WritePreparedBatchDeadline instead.
func (c *Conn) WritePreparedBatchDeadlineRetained(deadline time.Time, msgs []*PreparedMessage) (err error) {
	if len(msgs) == 0 {
		return nil
	}
	if c.client || c.flate() && !c.copts.serverNoContextTakeover {
		for _, m := range msgs {
			if err := c.WritePrepared(context.Background(), m); err != nil {
				return err
			}
		}
		return nil
	}
	return c.writePreparedBatchCore(nil, deadline, true, msgs)
}

func (c *Conn) writePreparedBatchCore(ctx context.Context, deadline time.Time, retainDeadline bool, msgs []*PreparedMessage) (err error) {
	// Resolve payloads (the compressed forms are shared and computed once)
	// before taking the frame lock.
	if cap(c.batchData) < len(msgs) {
		c.batchData = make([][]byte, len(msgs))
		c.batchRsv1 = make([]bool, len(msgs))
	} else {
		c.batchData = c.batchData[:len(msgs)]
		c.batchRsv1 = c.batchRsv1[:len(msgs)]
	}
	for i, m := range msgs {
		data := m.data
		c.batchRsv1[i] = c.flate() && len(data) >= c.flateThreshold
		if c.batchRsv1[i] {
			var err error
			data, err = m.deflate()
			if err != nil {
				return err
			}
		}
		c.batchData[i] = data
	}

	var lockCtx context.Context = ctx
	if lockCtx == nil {
		lockCtx = context.Background()
	}
	err = c.writeFrameMu.lock(lockCtx)
	if err != nil {
		return err
	}
	defer c.writeFrameMu.unlock()

	defer func() {
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				err = ctx.Err()
			} else if c.isClosed() {
				err = net.ErrClosed
			}
			err = fmt.Errorf("failed to write batch: %w", err)
		}
	}()

	c.closeStateMu.Lock()
	closeSentErr := c.closeSentErr
	c.closeStateMu.Unlock()
	if closeSentErr != nil {
		return net.ErrClosed
	}

	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	if ctx != nil {
		if c.setupWriteTimeout(ctx) {
			defer c.clearWriteTimeout()
		}
	} else if !deadline.IsZero() {
		// rwc is typed io.ReadWriteCloser for upstream compatibility; the
		// accepted/dialed values are always net.Conns (the hub's hijacked
		// TCP socket), which accept a write deadline. The retained variant
		// (WritePreparedBatchDeadlineRetained) deliberately does not clear
		// the deadline: the caller relies on it remaining armed to bound
		// later writes that arm no deadline of their own.
		if nc, ok := c.rwc.(net.Conn); ok {
			nc.SetWriteDeadline(deadline)
			if !retainDeadline {
				defer nc.SetWriteDeadline(time.Time{})
			}
		}
	}

	// Assemble frame headers + payload references into one vectored write.
	if cap(c.batchHeader) < len(msgs)*14 {
		c.batchHeader = make([]byte, len(msgs)*14)
	}
	if cap(c.batchBufs) < len(msgs)*2 {
		c.batchBufs = make(net.Buffers, 0, len(msgs)*2)
	}
	c.batchBufs = c.batchBufs[:0]
	scratch := c.batchHeader
	off := 0
	for i, m := range msgs {
		h := header{
			fin:           true,
			rsv1:          c.batchRsv1[i],
			opcode:        opcode(m.typ),
			payloadLength: int64(len(c.batchData[i])),
		}
		n := writeHeaderBytes(h, scratch[off:])
		off += n
		// The header is always emitted, even for empty payloads. writev
		// skips zero-length buffers, so appending it is safe.
		c.batchBufs = append(c.batchBufs, scratch[off-n:off])
		if len(c.batchData[i]) > 0 {
			c.batchBufs = append(c.batchBufs, c.batchData[i])
		}
	}

	// The batch is one vectored write: net.Buffers.WriteTo dispatches on
	// *net.TCPConn's unexported writeBuffers and emits a single writev
	// syscall. Wrappers that delegate Write transparently (fastserve's
	// upgrade-handoff replay conn) are peeled to the TCP connection first,
	// or the batch degrades to one write(2) per segment; anything without
	// an underlying TCP conn keeps the wrapped-conn fallback.
	_, err = c.batchBufs.WriteTo(writevTarget(c.rwc))
	return err
}

// writevTarget resolves the writer a vectored frame batch should go to.
//
// A raw *net.TCPConn accepts the batch as one writev syscall: net.Buffers
// dispatches on its unexported writeBuffers method, which no other type can
// provide. Transparent write-delegating wrappers (fastserve's
// prefixReplayConn ahead of the upgrade handoff) must be peeled or every
// segment becomes its own write(2), which at Campfire's cable fan-out
// (one batch per client per message) is a ~25 % throughput regression.
// Wrappers expose the underlying conn through the errors-style
// Unwrap() net.Conn convention; the peel is bounded so a degenerate wrapper
// cycle cannot loop. Targets that never resolve to a TCP conn (TLS conns,
// test doubles, pipes) keep the historical per-segment fallback, written
// through the wrapped conn itself.
func writevTarget(w io.Writer) io.Writer {
	for hops := 0; hops < 16; hops++ {
		if _, ok := w.(*net.TCPConn); ok {
			return w
		}
		u, ok := w.(interface{ Unwrap() net.Conn })
		if !ok {
			return w
		}
		next := u.Unwrap()
		if next == nil {
			return w
		}
		w = next
	}
	return w
}

// BatchMaxFrames bounds a single coalesced write. The hub drains at most
// this many queued frames per wake, keeping the batch small enough that the
// kernel-level vectored write stays single-syscall on Linux (IOV_MAX).
const BatchMaxFrames = 64
