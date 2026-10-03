//go:build !js

package websocket

import (
	"bytes"
	"context"
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
