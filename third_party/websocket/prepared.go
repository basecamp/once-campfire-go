//go:build !js

package websocket

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync"
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

// WritePrepared writes shared server frames, one per message, in order. Connections
// using context takeover and client connections retain the ordinary writer and its
// masking/history.
func (c *Conn) WritePrepared(ctx context.Context, messages ...*PreparedMessage) error {
	if len(messages) == 0 {
		return nil
	}
	if c.client || c.flate() && !c.copts.serverNoContextTakeover {
		for _, p := range messages {
			if err := c.Write(ctx, p.typ, p.data); err != nil {
				return err
			}
		}
		return nil
	}
	if err := c.msgWriter.reset(ctx, messages[0].typ); err != nil {
		return err
	}
	defer c.msgWriter.mu.unlock()
	frames := make([]preparedFrame, len(messages))
	for i, p := range messages {
		frames[i] = preparedFrame{opcode: opcode(p.typ), data: p.data}
		if c.flate() && len(p.data) >= c.flateThreshold {
			data, err := p.deflate()
			if err != nil {
				return err
			}
			frames[i].data, frames[i].flate = data, true
		}
	}
	return c.writePreparedFrames(ctx, frames)
}

type preparedFrame struct {
	opcode opcode
	flate  bool
	data   []byte
}

// writePreparedFrames is writeFrame for complete, unmasked server frames. When
// nothing is buffered it sends every header and payload in one vectored write instead
// of copying through the 4 KiB hijacked writer, which took two writes for a typical
// broadcast frame and one more for each queued frame. Locking, close state, timeouts
// and errors follow writeFrame.
func (c *Conn) writePreparedFrames(ctx context.Context, frames []preparedFrame) (err error) {
	if err = c.writeFrameMu.lock(ctx); err != nil {
		return err
	}
	if c.bw.Buffered() != 0 {
		c.writeFrameMu.unlock()
		for _, f := range frames {
			if _, err = c.writeFrame(ctx, true, f.flate, f.opcode, f.data); err != nil {
				return err
			}
		}
		return nil
	}
	defer c.writeFrameMu.unlock()
	defer func() {
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if c.isClosed() {
				err = net.ErrClosed
			}
			err = fmt.Errorf("failed to write frame: %w", err)
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
	if c.setupWriteTimeout(ctx) {
		defer c.clearWriteTimeout()
	}

	headers := make([]byte, 10*len(frames))
	buffers := make(net.Buffers, 0, 2*len(frames))
	for i, f := range frames {
		header := headers[10*i : 10*i+10]
		header[0] = 1<<7 | byte(f.opcode)
		if f.flate {
			header[0] |= 1 << 6
		}
		n := 2
		switch length := len(f.data); {
		case length > math.MaxUint16:
			header[1] = 127
			binary.BigEndian.PutUint64(header[2:], uint64(length))
			n = 10
		case length > 125:
			header[1] = 126
			binary.BigEndian.PutUint16(header[2:], uint16(length))
			n = 4
		default:
			header[1] = byte(length)
		}
		buffers = append(buffers, header[:n])
		if len(f.data) > 0 {
			buffers = append(buffers, f.data)
		}
	}
	_, err = buffers.WriteTo(c.rwc)
	return err
}
