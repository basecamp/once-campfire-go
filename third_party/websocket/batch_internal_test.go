//go:build !js

package websocket

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// recordingConn records what a server connection writes, and the write deadlines it sets.
type recordingConn struct {
	net.Conn
	writes    [][]byte
	deadlines []time.Time
}

func (r *recordingConn) Write(p []byte) (int, error) {
	r.writes = append(r.writes, bytes.Clone(p))
	return len(p), nil
}

func (r *recordingConn) SetWriteDeadline(t time.Time) error {
	r.deadlines = append(r.deadlines, t)
	return nil
}

func (r *recordingConn) Close() error { return nil }

func serverConn(rwc net.Conn, copts *compressionOptions) *Conn {
	return newConn(connConfig{rwc: rwc, copts: copts, flateThreshold: 256, br: bufio.NewReader(rwc), bw: bufio.NewWriter(rwc)})
}

type wireFrame struct {
	first   byte
	payload []byte
}

// parseServerFrames splits what a server wrote into frames, checking that none is masked.
func parseServerFrames(t *testing.T, b []byte) []wireFrame {
	t.Helper()
	var frames []wireFrame
	for len(b) > 0 {
		first, n := b[0], int(b[1]&0x7f)
		if b[1]&0x80 != 0 {
			t.Fatal("server frame is masked")
		}
		b = b[2:]
		switch n {
		case 126:
			n, b = int(binary.BigEndian.Uint16(b)), b[2:]
		case 127:
			n, b = int(binary.BigEndian.Uint64(b)), b[8:]
		}
		frames = append(frames, wireFrame{first, b[:n]})
		b = b[n:]
	}
	return frames
}

// inflateMessage decompresses a permessage-deflate payload: the sync-flush tail RFC 7692 strips,
// then an empty final block so the reader ends.
func inflateMessage(t *testing.T, p []byte) string {
	t.Helper()
	out, err := io.ReadAll(flate.NewReader(io.MultiReader(bytes.NewReader(p), strings.NewReader(deflateMessageTail+"\x01\x00\x00\xff\xff"))))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestWritePreparedBatchIsOneWriteUnderOneDeadline(t *testing.T) {
	small := NewPreparedMessage(MessageText, []byte(`{"type":"ping","message":1}`))
	big := NewPreparedMessage(MessageText, []byte(strings.Repeat("<div>message</div>", 200)))
	huge := NewPreparedMessage(MessageText, bytes.Repeat([]byte{'x'}, 70000))
	batch := []*PreparedMessage{small, big, small, big, huge}
	for _, copts := range []*compressionOptions{nil, CompressionNoContextTakeover.opts()} {
		rec := &recordingConn{}
		c := serverConn(rec, copts)
		deadline := time.Now().Add(time.Minute)
		if err := c.WritePreparedBatch(deadline, batch); err != nil {
			t.Fatal(err)
		}
		if len(rec.writes) != 1 {
			t.Fatalf("%d writes, want one", len(rec.writes))
		}
		if len(rec.deadlines) != 2 || !rec.deadlines[0].Equal(deadline) || !rec.deadlines[1].IsZero() {
			t.Fatalf("deadlines %v: want the batch's, then none", rec.deadlines)
		}
		frames := parseServerFrames(t, rec.writes[0])
		if len(frames) != len(batch) {
			t.Fatalf("%d frames, want %d", len(frames), len(batch))
		}
		for i, f := range frames {
			compressed := copts != nil && len(batch[i].data) >= 256
			first, text := byte(0x81), string(f.payload) // FIN, text
			if compressed {
				first, text = 0xc1, inflateMessage(t, f.payload) // FIN, RSV1, text
			}
			if f.first != first || text != string(batch[i].data) {
				t.Fatalf("frame %d: first byte %#x, payload match %v", i, f.first, text == string(batch[i].data))
			}
		}
	}
}

func TestWritePreparedBatchDeadline(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	c := serverConn(server, nil)
	defer c.CloseNow()
	frame := NewPreparedMessage(MessageText, []byte("hello"))

	// Nothing reads: the batch gives up at its deadline.
	err := c.WritePreparedBatch(time.Now().Add(50*time.Millisecond), []*PreparedMessage{frame})
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a client that doesn't read: %v", err)
	}

	// A batch that's read in time clears its deadline, so a later write isn't cut short by it.
	server, client = net.Pipe()
	defer client.Close()
	c = serverConn(server, nil)
	defer c.CloseNow()
	read := make(chan string, 2)
	go func() {
		for {
			b := make([]byte, 7)
			if _, err := io.ReadFull(client, b); err != nil {
				return
			}
			read <- string(b[2:])
		}
	}()
	if err := c.WritePreparedBatch(time.Now().Add(100*time.Millisecond), []*PreparedMessage{frame}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := c.Write(context.Background(), MessageText, []byte("again")); err != nil {
		t.Fatal(err)
	}
	if first, second := <-read, <-read; first != "hello" || second != "again" {
		t.Fatalf("read %q, %q", first, second)
	}
}
