//go:build !js

// ENGINE-54 socket-level tests: the deadline-bounded batched write must be
// wire-identical to the context-bounded one (the hub switched from one to
// the other; frame bytes and ordering are the contract), its deadline must
// reap a stalled write, and accepted connections must carry TCP_NODELAY.
package websocket

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWritePreparedBatchDeadlineMatchesContextBatch verifies the deadline
// variant emits byte-for-byte the same stream as the context variant over a
// corpus including compressed, binary, small and empty payloads.
func TestWritePreparedBatchDeadlineMatchesContextBatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msgs := []*PreparedMessage{
		NewPreparedMessage(MessageText, nil),
		NewPreparedMessage(MessageText, []byte("small")),
		NewPreparedMessage(MessageText, []byte(strings.Repeat("compressible ", 60))),
		NewPreparedMessage(MessageText, []byte(strings.Repeat("identical ", 10))),
		NewPreparedMessage(MessageBinary, []byte{0, 1, 2, 3}),
	}

	capture := func(deadline bool) []byte {
		server, client := newPipeConnPair(t, noContextTakeover(), 256)
		want := make(chan []byte, 1)
		go func() {
			var out []byte
			reader := bufio.NewReader(client.rwc.(net.Conn))
			for i := 0; i < len(msgs); i++ {
				h, err := readFrameHeader(reader, make([]byte, 8))
				if err != nil {
					t.Errorf("frame %d: %v", i, err)
					return
				}
				head := make([]byte, 14)
				head = head[:writeHeaderBytes(h, head)]
				out = append(out, head...)
				if h.payloadLength > 0 {
					raw := make([]byte, h.payloadLength)
					if _, err := io.ReadFull(reader, raw); err != nil {
						t.Errorf("frame %d payload: %v", i, err)
						return
					}
					out = append(out, raw...)
				}
			}
			want <- out
		}()
		var err error
		if deadline {
			err = server.WritePreparedBatchDeadline(time.Now().Add(10*time.Second), msgs)
		} else {
			err = server.WritePreparedBatch(ctx, msgs)
		}
		if err != nil {
			t.Fatal(err)
		}
		return <-want
	}

	ctxStream := capture(false)
	deadlineStream := capture(true)
	if !bytes.Equal(ctxStream, deadlineStream) {
		t.Fatalf("deadline and context batches differ:\nctx:      %x\ndeadline: %x", ctxStream, deadlineStream)
	}

	// A completed deadline-bounded write leaves the connection fully usable:
	// a second write through the same conn must succeed.
	server, client := newPipeConnPair(t, noContextTakeover(), 256)
	got := make(chan error, 2)
	go func() {
		reader := bufio.NewReader(client.rwc.(net.Conn))
		for round := 0; round < 2; round++ {
			for i := 0; i < len(msgs); i++ {
				h, err := readFrameHeader(reader, make([]byte, 8))
				if err != nil {
					got <- err
					return
				}
				if _, err := io.CopyN(io.Discard, reader, h.payloadLength); err != nil {
					got <- err
					return
				}
			}
			got <- nil
		}
	}()
	if err := server.WritePreparedBatchDeadline(time.Now().Add(10*time.Second), msgs); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	if err := server.WritePreparedBatchDeadline(time.Now().Add(10*time.Second), msgs); err != nil {
		t.Fatalf("second write after a completed deadline batch failed: %v", err)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

// TestWritePreparedBatchDeadlineReapsStalledWrite verifies an expired
// deadline returns an error instead of blocking forever: same shape as the
// context variant's timeout, only armed by the socket deadline.
func TestWritePreparedBatchDeadlineReapsStalledWrite(t *testing.T) {
	t.Parallel()

	server, client := newPipeConnPair(t, noContextTakeover(), 256)
	defer client.CloseNow()
	msgs := []*PreparedMessage{
		NewPreparedMessage(MessageText, []byte(strings.Repeat("stall me ", 512))),
	}
	start := time.Now()
	err := server.WritePreparedBatchDeadline(time.Now().Add(20*time.Millisecond), msgs)
	if err == nil {
		t.Fatal("stalled write with an expired deadline returned nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stalled write took %v to fail", elapsed)
	}
}

// TestAcceptSetsTCPNoDelay verifies accepted connections carry TCP_NODELAY:
// the ENGINE-54 contract (explicit in accept, matching the Rust reference's
// tokio sockets rather than relying on Go's runtime default).
func TestAcceptSetsTCPNoDelay(t *testing.T) {
	t.Parallel()

	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := accept(w, r, &AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			result <- err
			return
		}
		defer conn.CloseNow()
		tcp, ok := conn.rwc.(*net.TCPConn)
		if !ok {
			result <- fmt.Errorf("rwc is %T, want *net.TCPConn", conn.rwc)
			return
		}
		raw, err := tcp.SyscallConn()
		if err != nil {
			result <- err
			return
		}
		var sockErr error
		var on int
		if err := raw.Control(func(fd uintptr) {
			on, sockErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
		}); err != nil {
			result <- err
			return
		}
		if sockErr != nil {
			result <- sockErr
			return
		}
		if on != 1 {
			result <- fmt.Errorf("TCP_NODELAY = %d, want 1", on)
			return
		}
		result <- nil
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("accept handler did not complete")
	}
}
