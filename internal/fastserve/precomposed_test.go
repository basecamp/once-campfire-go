package fastserve

import (
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// memConn is an in-memory net.Conn for exercising the precomposed emission.
type memConn struct {
	mu   sync.Mutex
	buf  []byte
	done chan struct{}
}

func (c *memConn) Read(p []byte) (n int, err error) { <-c.done; return 0, nil }
func (c *memConn) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	c.buf = append(c.buf, p...)
	c.mu.Unlock()
	return len(p), nil
}
func (c *memConn) Close() error                     { close(c.done); return nil }
func (c *memConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (c *memConn) RemoteAddr() net.Addr             { return fakeAddr{} }
func (c *memConn) SetDeadline(time.Time) error      { return nil }
func (c *memConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "mem" }
func (fakeAddr) String() string  { return "mem" }

func TestWritePrecomposedBytes(t *testing.T) {
	srv := New(http.NotFoundHandler())
	conn := &memConn{done: make(chan struct{})}
	c := newConn(srv, conn)
	req, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.ProtoMajor, req.ProtoMinor = 1, 1
	req.Close = true
	res := c.newResponse(req)
	err = res.WritePrecomposed(200, []byte("X-Test: 1\r\n"), [][]byte{[]byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	out := string(conn.buf)
	wantHead := "HTTP/1.1 200 OK\r\nX-Test: 1\r\nDate: "
	if !contains(out, wantHead) {
		t.Fatalf("head missing: %q", out)
	}
	if !contains(out, "\r\nConnection: close\r\n\r\nhello") {
		t.Fatalf("close/terminator/body wrong: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && (sub == "" || index(s, sub) >= 0))
}
func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
