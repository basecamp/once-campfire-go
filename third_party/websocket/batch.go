//go:build !js

package websocket

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket/internal/bpool"
)

// WritePreparedBatch writes frames as whole messages, in order, in one socket write that must
// finish by deadline.
//
// On a server connection without context takeover, each message goes out as a few header bytes
// plus its shared payload (compressed at most once, for every connection), gathered into a single
// vectored write (writev on TCP) without copying payloads into the connection's buffer or arming a
// timer per frame. Other connections write the messages one at a time with WritePrepared.
func (c *Conn) WritePreparedBatch(deadline time.Time, frames []*PreparedMessage) error {
	if len(frames) == 0 {
		return nil
	}
	nc, ok := c.rwc.(net.Conn)
	if !ok || c.client || c.flate() && !c.copts.serverNoContextTakeover {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		for _, p := range frames {
			if err := c.WritePrepared(ctx, p); err != nil {
				return err
			}
		}
		return nil
	}

	// The same locks as a message write and a frame write, so control frames and other messages
	// never land inside the batch. Waiting for them counts against the deadline too.
	if err := lockBy(c.msgWriter.mu, deadline); err != nil {
		return err
	}
	defer c.msgWriter.mu.unlock()
	if err := lockBy(c.writeFrameMu, deadline); err != nil {
		return err
	}
	defer c.writeFrameMu.unlock()
	c.closeStateMu.Lock()
	closeSent := c.closeSentErr != nil
	c.closeStateMu.Unlock()
	if closeSent || c.isClosed() {
		return net.ErrClosed
	}

	scratch := batchScratchPool.Get().(*batchScratch)
	defer batchScratchPool.Put(scratch)
	// Room for every header up front, so the slices taken of it stay put.
	if need := maxServerHeader * len(frames); cap(scratch.headers) < need {
		scratch.headers = make([]byte, 0, need)
	}
	headers, bufs := scratch.headers[:0], scratch.bufs[:0]
	defer func() { clear(bufs); scratch.bufs = bufs[:0] }()
	for _, p := range frames {
		payload, compressed := p.data, false
		if c.flate() && len(payload) >= c.flateThreshold {
			var err error
			if payload, err = p.deflate(); err != nil {
				return err
			}
			compressed = true
		}
		start := len(headers)
		headers = appendServerHeader(headers, opcode(p.typ), compressed, len(payload))
		bufs = append(bufs, headers[start:], payload)
	}

	_ = nc.SetWriteDeadline(deadline)
	err := c.bw.Flush()
	if err == nil {
		err = writeBuffers(nc, bufs)
	}
	_ = nc.SetWriteDeadline(time.Time{})
	if err != nil {
		if c.isClosed() {
			err = net.ErrClosed
		}
		return fmt.Errorf("failed to write frames: %w", err)
	}
	return nil
}

// lockBy takes m, giving up at deadline; an uncontended lock is taken without arming a timer.
func lockBy(m *mu, deadline time.Time) error {
	if m.tryLock() {
		return nil
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return m.lock(ctx)
}

// maxServerHeader is the longest header of an unmasked frame.
const maxServerHeader = 10

type batchScratch struct {
	headers []byte
	bufs    net.Buffers
}

var batchScratchPool = sync.Pool{New: func() any { return new(batchScratch) }}

// appendServerHeader appends the header of a final, unmasked frame, as writeFrameHeader writes it.
func appendServerHeader(dst []byte, op opcode, rsv1 bool, length int) []byte {
	b := byte(1<<7) | byte(op)
	if rsv1 {
		b |= 1 << 6
	}
	switch {
	case length > math.MaxUint16:
		return binary.BigEndian.AppendUint64(append(dst, b, 127), uint64(length))
	case length > 125:
		return binary.BigEndian.AppendUint16(append(dst, b, 126), uint16(length))
	default:
		return append(dst, b, byte(length))
	}
}

// writeBuffers writes bufs with one writev where the connection supports it, and otherwise (TLS)
// as one contiguous write, so the batch still leaves in as few records as possible.
func writeBuffers(nc net.Conn, bufs net.Buffers) error {
	switch nc.(type) {
	case *net.TCPConn, *net.UnixConn:
		_, err := bufs.WriteTo(nc)
		return err
	}
	buf := bpool.Get()
	defer bpool.Put(buf)
	for _, b := range bufs {
		buf.Write(b)
	}
	_, err := nc.Write(buf.Bytes())
	return err
}
