//go:build !js

package websocket

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newPipeConnPair builds a server (header-first, readSrc) and client
// (buffered) connection over a synchronous net.Pipe.
func newPipeConnPair(t *testing.T, copts *compressionOptions, threshold int) (server, client *Conn) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	server = newConn(connConfig{
		rwc:            serverSide,
		client:         false,
		copts:          copts,
		flateThreshold: threshold,
		readSrc:        serverSide,
		bw:             bufio.NewWriterSize(serverSide, 4096),
	})
	client = newConn(connConfig{
		rwc:            clientSide,
		client:         true,
		copts:          copts,
		flateThreshold: threshold,
		br:             bufio.NewReader(clientSide),
		bw:             bufio.NewWriterSize(clientSide, 4096),
	})
	t.Cleanup(func() {
		server.CloseNow()
		client.CloseNow()
	})
	return server, client
}

func noContextTakeover() *compressionOptions {
	return CompressionNoContextTakeover.opts()
}

// TestWritePreparedBatchFrames verifies that a batched write emits exactly
// the same frames, in order, as individual WritePrepared calls would:
// correct headers (fin, rsv1 for compressed frames, payload lengths),
// shared-compression payloads, and order.
func TestWritePreparedBatchFrames(t *testing.T) {
	t.Parallel()

	server, client := newPipeConnPair(t, noContextTakeover(), 256)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	payloads := []string{
		"tiny",
		strings.Repeat("compress me ", 40), // 440 bytes: over the 256 threshold
		strings.Repeat("x", 200),           // over 125: 16-bit length
		"☃ unicode ☃",
		"",
	}

	var msgs []*PreparedMessage
	for _, p := range payloads {
		msgs = append(msgs, NewPreparedMessage(MessageText, []byte(p)))
	}

	// Parse the raw wire bytes on the client side concurrently: net.Pipe
	// writes block until the peer consumes them.
	parsed := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(client.rwc.(net.Conn))
		readBuf := make([]byte, 8)
		for i, want := range payloads {
			h, err := readFrameHeader(reader, readBuf)
			if err != nil {
				parsed <- fmt.Errorf("frame %d header: %w", i, err)
				return
			}
			if !h.fin {
				parsed <- fmt.Errorf("frame %d: not final", i)
				return
			}
			if h.opcode != opText {
				parsed <- fmt.Errorf("frame %d: opcode %v", i, h.opcode)
				return
			}
			wantCompressed := len(want) >= 256
			if h.rsv1 != wantCompressed {
				parsed <- fmt.Errorf("frame %d: rsv1 = %v, want %v (len %d)", i, h.rsv1, wantCompressed, len(want))
				return
			}
			raw := make([]byte, h.payloadLength)
			if _, err := io.ReadFull(reader, raw); err != nil {
				parsed <- fmt.Errorf("frame %d payload: %w", i, err)
				return
			}
			if !wantCompressed {
				if string(raw) != want {
					parsed <- fmt.Errorf("frame %d: payload %q, want %q", i, raw, want)
					return
				}
				continue
			}
			// Reconstruct the RFC 7692 trailer the sender omits. Like the
			// library's own reader, accept a truncated stream (no final
			// block) as long as the decoded payload matches.
			deflated := append(raw, deflateMessageTail...)
			fr := flate.NewReader(bytes.NewReader(deflated))
			got, err := io.ReadAll(fr)
			fr.Close()
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				parsed <- fmt.Errorf("frame %d inflate: %w", i, err)
				return
			}
			if string(got) != want {
				parsed <- fmt.Errorf("frame %d: decompressed %q, want %q", i, got, want)
				return
			}
		}
		parsed <- nil
	}()

	if err := server.WritePreparedBatch(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := <-parsed; err != nil {
		t.Fatal(err)
	}
	// Frame payloads must be identical to the shared compressed bytes.
	if len(msgs[1].compressed) == 0 {
		t.Fatal("compressed form was not computed and shared")
	}
}

// TestWritePreparedBatchEmptyMessage verifies that a zero-length message in a
// batch still emits its frame header (fragmentary writes must not vanish).
func TestWritePreparedBatchEmptyMessage(t *testing.T) {
	t.Parallel()

	server, client := newPipeConnPair(t, nil, 256)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	parsed := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(client.rwc.(net.Conn))
		readBuf := make([]byte, 8)
		h, err := readFrameHeader(reader, readBuf)
		if err != nil {
			parsed <- err
			return
		}
		if h.payloadLength != 0 {
			parsed <- fmt.Errorf("first frame payload length %d, want 0", h.payloadLength)
			return
		}
		h, err = readFrameHeader(reader, readBuf)
		if err != nil {
			parsed <- err
			return
		}
		if h.payloadLength != 1 {
			parsed <- fmt.Errorf("second frame payload length %d, want 1", h.payloadLength)
			return
		}
		raw := make([]byte, 1)
		if _, err := io.ReadFull(reader, raw); err != nil || raw[0] != 'a' {
			parsed <- fmt.Errorf("second frame payload %q: %v", raw, err)
			return
		}
		parsed <- nil
	}()

	if err := server.WritePreparedBatch(ctx, []*PreparedMessage{
		NewPreparedMessage(MessageText, nil),
		NewPreparedMessage(MessageText, []byte("a")),
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-parsed; err != nil {
		t.Fatal(err)
	}
}

// TestWritePreparedBatchMatchesSingleWrites verifies that a batched write
// emits byte-for-byte the same stream as sequential WritePrepared calls,
// including the compressed variants.
func TestWritePreparedBatchMatchesSingleWrites(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msgs := []*PreparedMessage{
		NewPreparedMessage(MessageText, []byte("small")),
		NewPreparedMessage(MessageText, []byte(strings.Repeat("compressible ", 60))),
		NewPreparedMessage(MessageBinary, []byte{0, 1, 2, 3}),
	}

	capture := func(batch bool) []byte {
		server, client := newPipeConnPair(t, noContextTakeover(), 256)
		// Parse the exact number of frames on a concurrent goroutine:
		// net.Pipe writes block until the peer consumes them.
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
		if batch {
			err = server.WritePreparedBatch(ctx, msgs)
		} else {
			for _, m := range msgs {
				if err = server.WritePrepared(ctx, m); err != nil {
					break
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		return <-want
	}

	batched := capture(true)
	sequential := capture(false)
	if !bytes.Equal(batched, sequential) {
		t.Fatalf("batch and sequential writes differ:\nbatched:    %x\nsequential: %x", batched, sequential)
	}
}

// TestWritePreparedBatchClientFallsBack verifies that client connections
// degrade to per-message writes.
func TestWritePreparedBatchClientFallsBack(t *testing.T) {
	t.Parallel()

	server, client := newPipeConnPair(t, noContextTakeover(), 256)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgs := []*PreparedMessage{
		NewPreparedMessage(MessageText, []byte("one")),
		NewPreparedMessage(MessageText, []byte("two")),
	}
	parsed := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server.rwc.(net.Conn))
		for i := 0; i < 2; i++ {
			h, err := readFrameHeader(reader, make([]byte, 8))
			if err != nil {
				parsed <- fmt.Errorf("client frame %d: %w", i, err)
				return
			}
			if !h.masked {
				parsed <- fmt.Errorf("client frame %d: not masked", i)
				return
			}
			if _, err := io.CopyN(io.Discard, reader, h.payloadLength); err != nil {
				parsed <- err
				return
			}
		}
		parsed <- nil
	}()
	if err := client.WritePreparedBatch(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := <-parsed; err != nil {
		t.Fatal(err)
	}
}

// TestServerReadExact verifies the header-first server read path: exact
// payloads for small and large messages, masked client frames, control
// frames interleaved, fragmented messages, and compressed messages.
func TestServerReadExact(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		copts *compressionOptions
	}{
		{"plain", nil},
		{"noContextTakeover", noContextTakeover()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, client := newPipeConnPair(t, tc.copts, 256)
			server.SetReadLimit(1 << 30)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			// Small (scratch-served), medium (16-bit length, over-scratch
			// direct read), and large payloads. Reads run on a separate
			// goroutine: net.Pipe writes block until the peer consumes them.
			msgs := []string{
				"hello",
				strings.Repeat("y", 600),
				strings.Repeat("z", 1<<16),
			}
			total := 3 + 1 + 1 // msgs + after-ping + fragmented
			if tc.copts != nil {
				total++ // compressed
			}
			type readResult struct {
				typ MessageType
				got []byte
				err error
			}
			reads := make(chan readResult, total)
			go func() {
				for i := 0; i < total; i++ {
					typ, got, err := server.Read(ctx)
					reads <- readResult{typ, got, err}
					if err != nil {
						return
					}
				}
			}()
			expect := func(want string) {
				t.Helper()
				select {
				case r := <-reads:
					if r.err != nil {
						t.Fatalf("read: %v", r.err)
					}
					if r.typ != MessageText || string(r.got) != want {
						t.Fatalf("read: %v %q, want %q", r.typ, r.got, want)
					}
				case <-ctx.Done():
					t.Fatalf("read timed out waiting for %q", want)
				}
			}

			for i, m := range msgs {
				if err := client.Write(ctx, MessageText, []byte(m)); err != nil {
					t.Fatalf("client write %d: %v", i, err)
				}
				expect(m)
			}

			// The next message arrives right after an interleaved frame.
			if err := client.Write(ctx, MessageText, []byte("after ping")); err != nil {
				t.Fatal(err)
			}
			expect("after ping")

			// Fragmented message: the fast path hands off to the streaming
			// reader and still returns the whole message.
			w, err := client.Writer(ctx, MessageText)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"frag", "ment", "ed"} {
				if _, err := w.Write([]byte(part)); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			expect("fragmented")

			// Compressed messages (client compresses over the threshold).
			if tc.copts != nil {
				big := strings.Repeat("compressible payload ", 64)
				if err := client.Write(ctx, MessageText, []byte(big)); err != nil {
					t.Fatal(err)
				}
				expect(big)
			}
		})
	}
}

// TestReadExactLimit verifies the exact read honors the connection read
// limit with the legacy error shape.
func TestReadExactLimit(t *testing.T) {
	t.Parallel()

	server, client := newPipeConnPair(t, nil, 256)
	server.SetReadLimit(1024)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- client.Write(ctx, MessageText, []byte(strings.Repeat("x", 4096)))
	}()
	_, _, err := server.Read(ctx)
	if !errors.Is(err, ErrMessageTooBig) {
		t.Fatalf("err %v, want ErrMessageTooBig", err)
	}
	if !strings.Contains(err.Error(), "read limited at 1025 bytes") {
		t.Fatalf("err %q, want read limited message", err)
	}
	client.CloseNow()
	<-writeDone
}

// TestAcceptHoldsNoReadBufferUntilFirstRead verifies the ENGINE-40 idle
// property: a freshly accepted server connection holds no buffered reader;
// the lazy scratch is created only after the first frame and stays small.
func TestAcceptHoldsNoReadBufferUntilFirstRead(t *testing.T) {
	t.Parallel()

	type result struct {
		conn *Conn
		err  error
	}
	accepted := make(chan result, 1)
	created := make(chan int, 1) // handler reports the scratch size after its first read
	proceed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r, nil)
		accepted <- result{conn, err}
		if err == nil {
			<-proceed // hold the connection idle until the test inspects it
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			_, _, _ = conn.Read(ctx) // first frame: allocates the scratch
			created <- conn.br.Size()
			conn.CloseNow()
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dialed, _, err := Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dialed.CloseNow()

	res := <-accepted
	if res.err != nil {
		t.Fatal(res.err)
	}
	conn := res.conn
	if conn.readSrc == nil {
		t.Fatal("server connection has no header-first read source")
	}
	if conn.br != nil {
		t.Fatalf("idle server connection holds a buffered reader (size %d)", conn.br.Size())
	}
	close(proceed)

	// The first frame allocates the small scratch in the handler; the
	// handler reports its size back through the channel.
	if err := dialed.Write(ctx, MessageText, []byte("wake")); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-created:
		if size > readScratchSize {
			t.Fatalf("scratch reader size %d exceeds %d", size, readScratchSize)
		}
	case <-ctx.Done():
		t.Fatal("scratch reader was not created after first read")
	}
}
