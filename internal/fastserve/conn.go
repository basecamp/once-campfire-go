package fastserve

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sebishogun/simdhttp/http1"
)

// conn is one accepted connection: the read side (persistent buffered reader
// over the prefix reader, so pipelined bytes stay inside the loop's own
// buffers), the parse state (reused across keep-alive requests), and the
// response side (segments written with one writev per flush).
type conn struct {
	srv *Server
	rwc net.Conn

	pr prefixReader
	br *bufio.Reader

	// head accumulates the current request head; parsed.Header aliases it.
	head        []byte
	readScratch [4096]byte

	// parse state, reused across requests (http1.Parse resets it).
	req        http1.Request
	lastMethod string
	// handedOff marks a connection delivered to the handoff server: the
	// serve loop no longer owns it and must not close it.
	handedOff bool
	// remoteAddr is the peer address, resolved once per connection.
	remoteAddr string
	// busy is true while a request is being served; Shutdown closes only
	// connections that are not busy (idle between requests).
	busy atomic.Bool

	// response side
	resp *response
	segs net.Buffers // pending wire segments, flushed with one writev
	// per-connection scratch for response bytes; all aliased segments are
	// consumed synchronously by flushOut before the next build reuses them.
	headBuf       []byte
	bodyBuf       []byte
	sortKeys      []string // header-sort scratch, reused across responses
	statusScratch [32]byte
	dateScratch   [64]byte
	lenScratch    [24]byte
	chunkScratch  [24]byte
}

// prefixReader serves the bytes a head parse pulled past the head end before
// the wire itself, so the body reader and the next request see the exact
// stream. pending aliases the conn's head buffer and is empty whenever head
// is reused (the drain guarantees the body is consumed before the next
// request starts).
type prefixReader struct {
	rwc     net.Conn
	pending []byte
}

func (p *prefixReader) Read(dst []byte) (int, error) {
	if len(p.pending) > 0 {
		n := copy(dst, p.pending)
		p.pending = p.pending[n:]
		if p.pending == nil { // drop the alias once empty
			p.pending = nil
		}
		return n, nil
	}
	return p.rwc.Read(dst)
}

func newConn(s *Server, rwc net.Conn) *conn {
	c := &conn{srv: s, rwc: rwc}
	c.pr.rwc = rwc
	c.br = bufio.NewReaderSize(&c.pr, 4096)
	// RemoteAddr().String() pays a JoinHostPort allocation; net/http caches
	// the address once per connection (conn.serve), so do the same.
	if ra := rwc.RemoteAddr(); ra != nil {
		c.remoteAddr = ra.String()
	}
	return c
}

// serve runs the keep-alive request loop for one connection.
func (c *conn) serve() {
	defer c.srv.connDone(c)
	defer func() {
		if !c.handedOff {
			c.rwc.Close() // handed-off conns are owned by the handoff server
		}
	}()
	first := true
	for {
		if c.srv.shuttingDown() {
			return // Shutdown: never start another request
		}
		if !first {
			// net/http: idle deadline while waiting for the next request's
			// first bytes; the idle clock starts after the response.
			if d := c.srv.IdleTimeout; d > 0 {
				c.rwc.SetReadDeadline(time.Now().Add(d))
			} else {
				c.rwc.SetReadDeadline(time.Time{})
			}
			if _, err := c.br.Peek(4); err != nil {
				return // silent close: timeout, EOF, or reset
			}
			c.rwc.SetReadDeadline(time.Time{})
		}
		first = false
		// RFC 7230 tolerance for old buggy clients: up to four CR/LF bytes
		// before a request that follows a POST (net/http parity).
		if c.lastMethod == "POST" {
			c.skipLeadingCRLF()
		}
		// Header deadline covers the whole head; the body deadline takes
		// over after the head is parsed.
		if d := c.srv.readHeaderTimeout(); d > 0 {
			c.rwc.SetReadDeadline(time.Now().Add(d))
		} else {
			c.rwc.SetReadDeadline(time.Time{})
		}
		consumed, err := c.readHead()
		if err != nil {
			if c.rejectHead(err) {
				return
			}
			continue // a preface fragment: keep reading
		}
		if c.srv.ReadTimeout > 0 {
			c.rwc.SetReadDeadline(time.Now().Add(c.srv.ReadTimeout))
		} else {
			c.rwc.SetReadDeadline(time.Time{})
		}
		// Bytes the head parse pulled past the head end belong to the body
		// (or the pipeline): serve them through the prefix reader before the
		// wire. pending aliases c.head, which is not reused until the body
		// is drained and the next request starts.
		c.pr.pending = c.head[consumed:]
		// Upgrade heads and the HTTP/2 preface leave this loop entirely:
		// the connection is handed to the internal net/http server, which
		// re-reads the consumed prefix and runs the same handler (hijacking
		// and h2c work unchanged through it).
		if c.handoffNeeded() {
			c.handoff(consumed)
			return
		}
		if !c.serveRequest() {
			return
		}
		// Keep-alive: reset the head buffer and parse state for the next
		// request; pipelined bytes already sit in br.
		c.head = c.head[:0]
	}
}

// readHead fills c.head until http1.Parse accepts the head or fails it. The
// single special continue case is the HTTP/2 preface arriving in fragments:
// its request line ("PRI * HTTP/2.0") parses as malformed, so while the
// buffer is a strict prefix of the preface the loop keeps reading instead of
// answering 400.
func (c *conn) readHead() (int, error) {
	for {
		consumed, err := http1.Parse(&c.req, c.head, http1.Compatible)
		if err == nil || err != http1.ErrIncomplete {
			// A malformed head that could still be an unfinished HTTP/2
			// preface ("PRI * HTTP/2.0" parses malformed the moment its
			// request line is complete) keeps reading until it diverges or
			// the 24-byte preface completes.
			if errors.Is(err, http1.ErrMalformed) && len(c.head) < len(h2Preface) && isPrefacePrefix(c.head) {
				// fall through to read more
			} else {
				return consumed, err
			}
		}
		if int64(len(c.head)) >= c.srv.maxHeadBytes() {
			return 0, errHeadTooLarge
		}
		n, rerr := c.br.Read(c.readScratch[:])
		if n > 0 {
			c.head = append(c.head, c.readScratch[:n]...)
		}
		if rerr != nil {
			return 0, rerr // EOF or deadline mid-head: silent close
		}
	}
}

var errHeadTooLarge = errors.New("fastserve: head exceeds limit")

// rejectHead maps a head-parse failure to the exact bytes net/http writes for
// the same input (conn.serve's literals), or closes silently. It reports
// false when the loop must instead read more bytes (a preface fragment).
// rejectHead maps a head-parse failure to the exact bytes net/http writes for
// the same input (conn.serve's literals), or closes silently. It reports
// false when the loop must instead read more bytes (a preface fragment).
func (c *conn) rejectHead(err error) bool {
	// A head that fails to parse as HTTP/1 but opens with the HTTP/2
	// preface is handed off — a client may send the preface and its first
	// SETTINGS frame in one write, so the match is a prefix, not equality.
	if errors.Is(err, http1.ErrMalformed) {
		if len(c.head) >= len(h2Preface) && bytes.HasPrefix(c.head, h2Preface) {
			c.handoff(0)
			return true
		}
		if len(c.head) < len(h2Preface) && isPrefacePrefix(c.head) {
			return false
		}
	}
	switch {
	case errors.Is(err, errHeadTooLarge),
		errors.Is(err, http1.ErrHeadTooLarge),
		errors.Is(err, http1.ErrTooManyHeaders),
		errors.Is(err, http1.ErrValueTooLarge),
		errors.Is(err, http1.ErrRequestLineTooLarge):
		// net/http answers 431 for any head past the server's limit; the
		// simdhttp Compatible profile's stricter per-field caps land here
		// too, which is the documented 431-vs-net/http delta.
		c.reject("HTTP/1.1 431 Request Header Fields Too Large",
			"431 Request Header Fields Too Large")
		return true
	case errors.Is(err, http1.ErrMissingHost):
		c.reject("HTTP/1.1 400 Bad Request: missing required Host header",
			"400 Bad Request: missing required Host header")
		return true
	case err == io.EOF, isNetTimeout(err):
		return true // silent close (net/http's common-net-read-error path)
	default:
		c.reject("HTTP/1.1 400 Bad Request", "400 Bad Request")
		return true
	}
}

// reject writes net/http's verbatim error response and closes the connection
// write-side after a short RST-avoidance wait (net/http's closeWriteAndWait).
func (c *conn) reject(statusLine, body string) {
	b := make([]byte, 0, len(statusLine)+len(body)+64)
	b = append(b, statusLine...)
	b = append(b, "\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n"...)
	b = append(b, body...)
	c.rawWrite(b)
	c.closeWriteAndWait()
}

const rstAvoidanceDelay = 500 * time.Millisecond

func (c *conn) closeWriteAndWait() {
	if tcp, ok := c.rwc.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}
	time.Sleep(rstAvoidanceDelay)
}

func isNetTimeout(err error) bool {
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// skipLeadingCRLF discards up to four leading CR or LF bytes before a request
// that follows a POST (net/http's numLeadingCRorLF tolerance).
func (c *conn) skipLeadingCRLF() {
	peek, err := c.br.Peek(4)
	if err != nil {
		return
	}
	n := 0
	for n < len(peek) && (peek[n] == '\r' || peek[n] == '\n') {
		n++
	}
	if n > 0 {
		c.br.Discard(n)
	}
}

// handoff hands the connection to the internal net/http server, replaying
// every byte this loop consumed: the parsed head, the bytes the head read
// past its end, and whatever the buffered reader pulled from the wire.
func (c *conn) handoff(consumed int) {
	buffered, _ := c.br.Peek(c.br.Buffered())
	prefix := make([]byte, 0, len(c.head)+len(buffered))
	prefix = append(prefix, c.head[:consumed]...)
	prefix = append(prefix, c.head[consumed:]...)
	prefix = append(prefix, buffered...)
	if c.srv.handoffConn(newPrefixReplayConn(c.rwc, prefix)) {
		// The handoff server owns this connection now; the serve loop must
		// not close it on the way out.
		c.handedOff = true
	}
}

// handoffNeeded reports whether the parsed head asks for a protocol switch
// (any Upgrade header) — such connections are served by the internal
// net/http server so hijacking keeps working identically.
func (c *conn) handoffNeeded() bool {
	for i := range c.req.Headers {
		if bytes.EqualFold(c.req.Headers[i].Name, []byte("Upgrade")) {
			return true
		}
	}
	return false
}

// serveRequest builds the *http.Request, runs the handler, and finishes the
// response. It reports whether the connection may serve another request.
func (c *conn) serveRequest() bool {
	c.busy.Store(true)
	defer c.busy.Store(false)
	req, err := c.newRequest(&c.req, c.br)
	if err != nil {
		c.reject("HTTP/1.1 400 Bad Request", "400 Bad Request")
		return false
	}
	res := c.newResponse(req)
	c.resp = res
	// Write deadline set before the handler runs, like net/http, so slow
	// handlers expire when the response eventually writes.
	if d := c.srv.WriteTimeout; d > 0 {
		c.rwc.SetWriteDeadline(time.Now().Add(d))
	} else {
		c.rwc.SetWriteDeadline(time.Time{})
	}
	c.lastMethod = req.Method

	// Expect handling, mirroring conn.serve: exactly "100-continue" wraps
	// the body; any other Expect answers 417 and closes.
	if expectsContinue(req.Header) {
		if protoAtLeast(req.ProtoMajor, req.ProtoMinor, 1, 1) && req.ContentLength != 0 {
			req.Body = &continueReader{ReadCloser: req.Body, c: c}
			res.canWriteContinue = true
		}
	} else if req.Header.Get("Expect") != "" {
		res.Header().Set("Connection", "close")
		res.WriteHeader(http.StatusExpectationFailed)
		c.resp = nil
		res.finish()
		return false
	}

	c.srv.serveHandler(res, req)
	c.resp = nil
	res.finish()
	return res.shouldKeepAlive()
}

// maybeWriteContinue implements the 100-continue rule at the body's first
// read: nothing was written yet (canWriteContinue), so the interim response
// goes out before any response byte.
func (c *conn) maybeWriteContinue() {
	if r := c.resp; r != nil && r.canWriteContinue {
		r.canWriteContinue = false
		c.rawWrite([]byte("HTTP/1.1 100 Continue\r\n\r\n"))
	}
}

// rawWrite writes b with the active write deadline, bypassing the response
// segment pipeline (interim responses and rejections).
func (c *conn) rawWrite(b []byte) error {
	_, err := c.rwc.Write(b)
	return err
}

// emit adds a wire segment. Segments alias per-connection scratch buffers
// and the response body buffer; flushOut consumes them synchronously before
// any alias is rebuilt.
func (c *conn) emit(b []byte) {
	c.segs = append(c.segs, b)
}

// flushOut writes all pending segments with one writev (net.Buffers) and
// clears the list, keeping the backing array for the next response.
func (c *conn) flushOut() error {
	if len(c.segs) == 0 {
		return nil
	}
	segs := c.segs
	c.segs = c.segs[:0]
	_, err := segs.WriteTo(c.rwc)
	return err
}

// newResponse allocates the per-request writer state on top of per-conn
// storage. handlerHeader is the one per-request heap allocation the loop
// cannot avoid: the application handler reads and writes it as an http.Header
// map (net/http allocates it per request as well).
func (c *conn) newResponse(req *http.Request) *response {
	w := &response{
		c:                c,
		req:              req,
		handlerHeader:    make(http.Header, 8),
		contentLength:    -1,
		wantsClose:       req.Close,
		wants10KeepAlive: wantsHTTP10KeepAlive(req.ProtoMajor, req.ProtoMinor, req.Header),
	}
	w.body = c.bodyBuf[:0]
	w.cw.res = w
	return w
}
