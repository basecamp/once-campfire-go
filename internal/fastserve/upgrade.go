package fastserve

import (
	"bytes"
	"net"
)

// prefixReplayConn serves bytes already read from a connection before the
// wrapped connection itself, so a net/http.Server handed a connection this
// loop already parsed a request head from sees every byte of it again.
//
// The party that consumed the head must deliver exactly the bytes it consumed
// from the wire: the parsed head plus any bytes the buffered reader pulled in
// beyond it (the beginning of the request body, pipelined requests, or the
// HTTP/2 preface). Anything between the last supplied prefix byte and the
// first wrapped-conn byte is lost to the new owner, so the loop always replays
// the full consumed prefix.
type prefixReplayConn struct {
	net.Conn
	prefix *bytes.Reader
	// done is closed once the prefix is exhausted, so the wrapper can stop
	// consulting it; the wrapped conn is owned by the reader after that.
	done bool
}

func newPrefixReplayConn(c net.Conn, prefix []byte) *prefixReplayConn {
	return &prefixReplayConn{Conn: c, prefix: bytes.NewReader(prefix)}
}

func (c *prefixReplayConn) Read(p []byte) (int, error) {
	if !c.done && c.prefix.Len() > 0 {
		n, err := c.prefix.Read(p)
		if err == nil && c.prefix.Len() == 0 {
			c.done = true
		}
		if n > 0 {
			return n, nil
		}
	}
	c.done = true
	return c.Conn.Read(p)
}

// queueListener is the accept side of the upgrade handoff: fastserve hands a
// connection off by pushing it here, and the internal net/http.Server's
// Serve loop Accepts it. Close makes Accept return ErrClosed, ending Serve.
type queueListener struct {
	ch     chan net.Conn
	closed chan struct{}
	addr   net.Addr
}

func newQueueListener(addr net.Addr) *queueListener {
	return &queueListener{ch: make(chan net.Conn), closed: make(chan struct{}), addr: addr}
}

func (l *queueListener) push(c net.Conn) bool {
	select {
	case <-l.closed:
		return false
	default:
	}
	select {
	case l.ch <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *queueListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *queueListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *queueListener) Addr() net.Addr { return l.addr }
