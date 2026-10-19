//go:build !js

package websocket

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// unwrapConn is a transparent write-delegating wrapper, the shape of
// fastserve's prefixReplayConn ahead of the upgrade handoff.
type unwrapConn struct {
	net.Conn
}

func (c *unwrapConn) Unwrap() net.Conn { return c.Conn }

// tcpPair returns a connected TCP pair (the shape net/http hijacks in
// production) plus a cleanup that closes both ends.
func tcpPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		c, err := ln.Accept()
		accepted <- acceptResult{c, err}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p := <-accepted
	if p.err != nil {
		client.Close()
		t.Fatal(p.err)
	}
	server = p.conn
	t.Cleanup(func() {
		server.Close()
		client.Close()
	})
	return server, client
}

// TestWritevTargetPeelsTransparentWrappers: a vectored batch must resolve to
// the underlying *net.TCPConn through any number of write-delegating Unwrap
// hops, and stay put when the target is not a TCP conn at all (TLS, pipes,
// test doubles), where the historical per-segment fallback still applies.
func TestWritevTargetPeelsTransparentWrappers(t *testing.T) {
	srv, _ := tcpPair(t)
	tcp, ok := srv.(*net.TCPConn)
	if !ok {
		t.Fatalf("pair end is %T, want *net.TCPConn", srv)
	}

	if got := writevTarget(tcp); got != tcp {
		t.Fatalf("raw TCP target: got %T, want the conn itself", got)
	}

	wrapped := &unwrapConn{Conn: tcp}
	if got := writevTarget(wrapped); got != tcp {
		t.Fatalf("one-hop wrapper: got %T, want the TCP conn", got)
	}

	deep := &unwrapConn{Conn: &unwrapConn{Conn: wrapped}}
	if got := writevTarget(deep); got != tcp {
		t.Fatalf("multi-hop wrapper: got %T, want the TCP conn", got)
	}

	var plain bytes.Buffer
	if got := writevTarget(&plain); got != &plain {
		t.Fatalf("non-unwrappable writer must be returned as-is, got %T", got)
	}

	cycle := &unwrapConn{}
	cycle.Conn = cycle
	if got := writevTarget(cycle); got != cycle {
		t.Fatalf("wrapper cycle must terminate on the wrapper, got %T", got)
	}

	nilUnwrap := &unwrapConn{}
	if got := writevTarget(nilUnwrap); got != nilUnwrap {
		t.Fatalf("nil Unwrap must return the wrapper, got %T", got)
	}
}

// TestWritePreparedBatchThroughWrappedConn: a server Conn whose rwc is a
// transparent wrapper (the upgrade-handoff shape) must deliver a batch
// byte-identically to an unwrapped conn. The peel resolves the writev
// target but must not change what reaches the wire.
func TestWritePreparedBatchThroughWrappedConn(t *testing.T) {
	msgs := []*PreparedMessage{
		NewPreparedMessage(MessageText, []byte("first")),
		NewPreparedMessage(MessageBinary, []byte{0, 1, 2, 3}),
		NewPreparedMessage(MessageText, []byte("last")),
	}

	readBatch := func(rwc net.Conn) []byte {
		t.Helper()
		rwc.SetReadDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(rwc)
		var out []byte
		for i := 0; i < len(msgs); i++ {
			h, err := readFrameHeader(reader, make([]byte, 8))
			if err != nil {
				t.Fatalf("frame %d: %v", i, err)
			}
			head := make([]byte, 14)
			head = head[:writeHeaderBytes(h, head)]
			out = append(out, head...)
			if h.payloadLength > 0 {
				raw := make([]byte, h.payloadLength)
				if _, err := io.ReadFull(reader, raw); err != nil {
					t.Fatalf("frame %d payload: %v", i, err)
				}
				out = append(out, raw...)
			}
		}
		return out
	}

	raw, client := tcpPair(t)
	wrapped := &unwrapConn{Conn: raw}
	c := newConn(connConfig{rwc: wrapped, client: false})
	defer c.CloseNow()
	if err := c.WritePreparedBatchDeadline(time.Now().Add(5*time.Second), msgs); err != nil {
		t.Fatalf("batch through wrapper: %v", err)
	}
	want := readBatch(client)

	// Same batch on an unwrapped conn: identical wire bytes.
	raw2, client2 := tcpPair(t)
	c2 := newConn(connConfig{rwc: raw2, client: false})
	defer c2.CloseNow()
	if err := c2.WritePreparedBatchDeadline(time.Now().Add(5*time.Second), msgs); err != nil {
		t.Fatalf("batch unwrapped: %v", err)
	}
	got := readBatch(client2)
	if !bytes.Equal(got, want) {
		t.Fatalf("wrapped and unwrapped batches differ:\nwrapped:   %x\nunwrapped: %x", want, got)
	}
}
