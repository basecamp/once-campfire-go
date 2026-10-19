package fastserve

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type writeOnlyFaultConn struct{ net.Conn }

func (c writeOnlyFaultConn) Write([]byte) (int, error) {
	return 0, errors.New("write transport failed")
}

func TestWriteFailureCancelsHandlerContext(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan bool, 1)
	s := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err == nil {
			done <- false
			return
		}
		select {
		case <-r.Context().Done():
			done <- true
		case <-time.After(time.Second):
			done <- false
		}
	}))
	c := newConn(s, writeOnlyFaultConn{server})
	s.wg.Add(1)
	exited := make(chan struct{})
	go func() { c.serve(); close(exited) }()
	client.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Keep the client's write side open: the server's disconnect reader is
	// independently blocked and cannot supply request cancellation.
	if !<-done {
		t.Error("write failure did not cancel active handler context")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not exit")
	}
}

type failedCloseBody struct{ io.Reader }

func (failedCloseBody) Close() error { return errors.New("undrained body") }

func TestFailedBodyClosePreventsReuse(t *testing.T) {
	c := newConn(New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), &prefixFaultConn{in: strings.NewReader("")})
	r, _ := http.NewRequest("GET", "http://example.com/", nil)
	r.Body = failedCloseBody{strings.NewReader("")}
	w := c.newResponse(r)
	w.WritePrecomposed(200, []byte("Content-Length: 0\r\n"), nil)
	w.finish()
	if !w.closeAfterReply {
		t.Fatal("failed body close permits reuse")
	}
}
