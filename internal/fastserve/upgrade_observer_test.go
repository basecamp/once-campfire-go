package fastserve

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestObserverBytePreservedAcrossPipelinedUpgrade(t *testing.T) {
	server, client := net.Pipe()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	probe := &observerProbeConn{server, make(chan struct{}, 4), make(chan struct{}, 4)}
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	// A complete masked one-byte WebSocket text frame. Its first byte must
	// come from the disconnect observer rather than the head/body reader.
	frame := []byte{0x81, 0x81, 1, 2, 3, 4, 'x' ^ 1}
	type readResult struct {
		data []byte
		err  error
	}
	upgraded := make(chan readResult, 1)
	srv := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			<-release
			io.WriteString(w, "first")
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upgraded <- readResult{err: err}
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		if err := rw.Flush(); err != nil {
			upgraded <- readResult{err: err}
			return
		}
		data := make([]byte, len(frame))
		n, err := io.ReadFull(rw.Reader, data)
		upgraded <- readResult{data: data[:n], err: err}
	}))
	c := newConn(srv, probe)
	srv.wg.Add(1)
	ended := make(chan struct{})
	t.Cleanup(func() {
		unblock()
		client.Close()
		server.Close()
		srv.Close()
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Error("connection owner did not stop")
		}
	})
	go func() { c.serve(); close(ended) }()
	pipeline := "GET /first HTTP/1.1\r\nHost: x\r\n\r\n" +
		"GET /upgrade HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
	if _, err := io.WriteString(client, pipeline); err != nil {
		t.Fatal(err)
	}
	awaitObserver(t, probe.started)
	if _, err := client.Write(frame[:1]); err != nil {
		t.Fatal(err)
	}
	awaitObserver(t, probe.finished)
	unblock()
	reader := bufio.NewReader(client)
	first, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(first.Body)
	first.Body.Close()
	if err != nil || first.StatusCode != http.StatusOK || string(body) != "first" {
		t.Fatalf("first response: %d %q %v", first.StatusCode, body, err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status: %d", response.StatusCode)
	}
	if _, err := client.Write(frame[1:]); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-upgraded:
		if result.err != nil || !bytes.Equal(result.data, frame) {
			t.Fatalf("upgraded frame = %x, want %x: %v", result.data, frame, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgraded stream did not finish reading its first frame")
	}
	awaitObserver(t, ended)
	if c.observer.hasByte {
		t.Fatal("handoff retained the transferred observer byte")
	}
}

type replayReadSpy struct {
	net.Conn
	reads int
}

func (c *replayReadSpy) Read([]byte) (int, error) {
	c.reads++
	return 0, io.EOF
}

func TestPrefixReplayConnReleasesConsumedBuffer(t *testing.T) {
	raw := &replayReadSpy{}
	prefix := bytes.Repeat([]byte{'x'}, 64<<10)
	wrapped := newPrefixReplayConn(raw, prefix)
	got := make([]byte, len(prefix))
	if _, err := io.ReadFull(wrapped, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, prefix) {
		t.Fatal("replay changed the buffered bytes")
	}
	if wrapped.prefix != nil {
		t.Fatal("consumed handshake buffer remains referenced by the upgraded connection")
	}
	if raw.reads != 0 {
		t.Fatal("prefix replay unexpectedly read from the socket")
	}
}

func TestPrefixReplayConnZeroLengthReadPreservesPrefix(t *testing.T) {
	raw := &replayReadSpy{}
	wrapped := newPrefixReplayConn(raw, []byte("head"))
	if n, err := wrapped.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length read: %d, %v", n, err)
	}
	if raw.reads != 0 {
		t.Fatal("zero-length prefix read reached the socket")
	}
	var got [4]byte
	if _, err := io.ReadFull(wrapped, got[:]); err != nil || string(got[:]) != "head" {
		t.Fatalf("prefix after zero-length read: %q, %v", got, err)
	}
}
