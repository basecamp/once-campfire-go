package fastserve

import (
	"bufio"
	"bytes"
	"context"
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

	pr       prefixReader
	br       *bufio.Reader
	observer disconnectReader

	// head accumulates the current request head; parsed.Header aliases it.
	head        []byte
	readScratch [4096]byte
	// drainScratch serves the post-handler request-body drain; the 8 KiB
	// array lives on the connection because an interface call would push a
	// stack copy to the heap once per drain.
	drainScratch [8 << 10]byte

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
	// readArmed/writeArmed mirror the socket's deadline state in userspace, so
	// a deadline call that would leave the socket exactly as it is is skipped
	// (the C port's shape: a per-connection deadline record, and only a change
	// reaches the runtime poller). Every deadline touch on this connection
	// goes through setReadDeadline/setWriteDeadline, and the socket is owned by
	// this loop until it is handed off, so the mirror cannot go stale.
	//
	// Precondition: the connection arrives with no deadline armed, so the
	// mirror starts false. Server.Serve accepts from the bare net.Listener it
	// was given (net/http's accept path arms no deadline either), and a
	// wrapper listener that pre-arms one — a handshake-timeout wrapper around
	// Accept, say — must clear it before handing the conn over, or the first
	// clear here is skipped as redundant and the stale deadline survives.
	readArmed  bool
	writeArmed bool

	// response side
	resp     *response
	writeErr error       // first transport write failure; never cleared on keep-alive
	segs     net.Buffers // pending wire segments, flushed with one writev
	// per-connection scratch for response bytes; all aliased segments are
	// consumed synchronously by flushOut before the next build reuses them.
	headBuf       []byte
	bodyBuf       []byte
	sortKeys      []string // header-sort scratch, reused across responses
	statusScratch [32]byte
	dateScratch   [64]byte
	lenScratch    [24]byte
	chunkScratch  [24]byte
	// dateLine is the formatted Date value, refreshed at most once per second
	// (ENGINE-49); dateSecond is the Unix second it was formatted for.
	dateLine   []byte
	dateSecond int64
}

// prefixReader serves the bytes a head parse pulled past the head end before
// the wire itself, so the body reader and the next request see the exact
// stream. pending aliases the conn's head buffer and is empty whenever head
// is reused (the drain guarantees the body is consumed before the next
// request starts).
type prefixReader struct {
	rwc      net.Conn
	pending  []byte
	observer *disconnectReader
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
	if p.observer != nil && p.observer.hasByte {
		if len(dst) == 0 {
			return 0, nil
		}
		dst[0] = p.observer.byte[0]
		p.observer.hasByte = false
		return 1, nil
	}
	n, err := p.rwc.Read(dst)
	if err != nil && p.observer != nil && p.observer.cancel != nil {
		p.observer.cancel()
	}
	return n, err
}

func newConn(s *Server, rwc net.Conn) *conn {
	// The deadline mirror (readArmed/writeArmed) starts cleared, which is the
	// state of every connection Serve accepts from a bare listener; see the
	// field comment for the precondition a wrapped listener must meet.
	c := &conn{srv: s, rwc: rwc}
	c.pr.rwc = rwc
	c.pr.observer = &c.observer
	c.observer.c = c
	c.br = bufio.NewReaderSize(&c.pr, 4096)
	// RemoteAddr().String() pays a JoinHostPort allocation; net/http caches
	// the address once per connection (conn.serve), so do the same.
	if ra := rwc.RemoteAddr(); ra != nil {
		c.remoteAddr = ra.String()
	}
	return c
}

// setReadDeadline arms the socket read deadline at t, or clears it when t is
// zero, and records the state the socket is left in. A zero t against an
// already-cleared deadline is skipped: net/http's readRequest and conn.serve
// re-clear already-clear deadlines on every request, this loop inherited that
// call shape, and the mirror removes the repeated deadline call (and the
// runtime-poller work behind it) without changing what the socket holds —
// clearing a deadline that is already unset leaves it unset. A non-zero t is
// always applied, because the absolute instant moves with every request. The
// mirror moves only when the setter succeeds, so a failed call leaves it
// still matching the socket.
func (c *conn) setReadDeadline(t time.Time) {
	if t.IsZero() && !c.readArmed {
		return
	}
	if c.rwc.SetReadDeadline(t) == nil {
		c.readArmed = !t.IsZero()
	}
}

// setWriteDeadline is setReadDeadline for the write side. net/http clears the
// write deadline after every response even when WriteTimeout is zero and it
// never armed one; that clear is skipped here whenever this loop has no write
// deadline armed, and kept whenever it has (a stale write deadline must not
// outlive the response it bounded: reject() writes the 400/431 tail before the
// next request arms its own deadline).
func (c *conn) setWriteDeadline(t time.Time) {
	if t.IsZero() && !c.writeArmed {
		return
	}
	if c.rwc.SetWriteDeadline(t) == nil {
		c.writeArmed = !t.IsZero()
	}
}

// serve runs the keep-alive request loop for one connection.
func (c *conn) serve() {
	defer c.srv.connDone(c)
	defer c.observer.close()
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
				c.setReadDeadline(time.Now().Add(d))
			} else {
				c.setReadDeadline(time.Time{})
			}
			if _, err := c.br.Peek(4); err != nil {
				return // silent close: timeout, EOF, or reset
			}
			// The header deadline set below replaces the idle deadline
			// immediately; nothing reads between the two sets, so net/http's
			// intermediate clear was a dead syscall (ENGINE-62).
		}
		first = false
		// RFC 7230 tolerance for old buggy clients: up to four CR/LF bytes
		// before a request that follows a POST (net/http parity).
		if c.lastMethod == "POST" {
			c.skipLeadingCRLF()
		}
		// net/http's readRequest deadline block, clock and all: both deadlines
		// are measured from t0, so ReadTimeout covers head and body together
		// and a head that consumes the budget leaves the body only the
		// remainder. The header deadline goes on first; the whole-request
		// deadline is re-armed only when it differs (net/http:
		// `if !hdrDeadline.Equal(wholeReqDeadline)`), which with the default
		// wiring — ReadHeaderTimeout falling back to ReadTimeout — is never,
		// because both are t0+ReadTimeout.
		t0 := time.Now()
		var hdrDeadline, wholeReqDeadline time.Time
		if d := c.srv.readHeaderTimeout(); d > 0 {
			hdrDeadline = t0.Add(d)
		}
		if d := c.srv.ReadTimeout; d > 0 {
			wholeReqDeadline = t0.Add(d)
		}
		c.setReadDeadline(hdrDeadline)
		consumed, err := c.readHead()
		if err != nil {
			if c.rejectHead(err) {
				return
			}
			continue // a preface fragment: keep reading
		}
		if !hdrDeadline.Equal(wholeReqDeadline) {
			c.setReadDeadline(wholeReqDeadline)
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
// past its end, whatever the buffered reader pulled from the wire, and a
// byte held by the disconnect observer beyond those buffered bytes.
func (c *conn) handoff(consumed int) {
	c.observer.stop()
	buffered, _ := c.br.Peek(c.br.Buffered())
	observed := 0
	if c.observer.hasByte {
		observed = 1
	}
	prefix := make([]byte, 0, len(c.head)+len(buffered)+observed)
	prefix = append(prefix, c.head[:consumed]...)
	prefix = append(prefix, c.head[consumed:]...)
	prefix = append(prefix, buffered...)
	if c.observer.hasByte {
		prefix = append(prefix, c.observer.byte[0])
		c.observer.hasByte = false
	}
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
	// The per-request cancellable context mirrors net/http's
	// conn.readRequest (ctx, cancelCtx := context.WithCancel(ctx); the
	// request sees it through req.ctx): handlers that watch
	// r.Context().Done() must wake when their request is over. The deferred
	// cancel also covers the early-return paths below, so a malformed
	// request never leaves an uncancelled child on the background context.
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.observer.cancel = cancel
	defer func() { c.observer.stop(); c.observer.cancel = nil }()
	req, err := c.newRequest(&c.req, c.br, reqCtx)
	if err != nil {
		c.reject("HTTP/1.1 400 Bad Request", "400 Bad Request")
		return false
	}
	res := c.newResponse(req)
	c.resp = res
	// Write deadline set before the handler runs, like net/http, so slow
	// handlers expire when the response eventually writes. net/http arms
	// this only when WriteTimeout is set (readRequest's defer); with no
	// WriteTimeout there is nothing to arm and nothing to clear here.
	if d := c.srv.WriteTimeout; d > 0 {
		c.setWriteDeadline(time.Now().Add(d))
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
		cancel()
		res.finish()
		return false
	}

	if req.Body == http.NoBody || req.ContentLength == 0 {
		c.observer.start()
	}
	c.srv.serveHandler(res, req)
	cancel() // net/http cancels the request context right after ServeHTTP
	c.observer.stop()
	c.observer.cancel = nil // body draining must not start another observer
	c.resp = nil
	if err := res.finish(); err != nil {
		return false
	}
	// net/http clears the write deadline after finishRequest; a keep-alive
	// connection must not carry the previous request's deadline into the
	// 400/431 tail that reject() may write before the next request arms its
	// own. Skipped when this loop never armed one.
	c.setWriteDeadline(time.Time{})
	keep := res.shouldKeepAlive()
	if !keep && res.requestBodyLimitHit {
		// net/http finishes a connection whose body hit the limit (or was
		// left unread past the drain budget) with the RST-avoidance close.
		c.closeWriteAndWait()
	}
	return keep
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
	if c.writeErr != nil {
		return c.writeErr
	}
	n, err := c.rwc.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return c.recordWriteError(err)
}

func (c *conn) recordWriteError(err error) error {
	if c.writeErr == nil && err != nil {
		c.writeErr = err
		if c.observer.cancel != nil {
			c.observer.cancel()
		}
	}
	return c.writeErr
}

// emit adds a wire segment. Segments alias per-connection scratch buffers
// and the response body buffer; flushOut consumes them synchronously before
// any alias is rebuilt.
func (c *conn) emit(b []byte) {
	if c.writeErr != nil {
		return
	}
	c.segs = append(c.segs, b)
}

// flushOut writes all pending segments with one writev (net.Buffers) and
// clears the list. The receiver is the conn's own field, so the writev
// dispatch takes no per-flush address-of-local (no extra allocation per
// flush); WriteTo consumes the list front to back, so the origin header is
// kept alongside and the list resets to it with full capacity for the next
// response.
func (c *conn) flushOut() error {
	if c.writeErr != nil {
		return c.writeErr
	}
	if len(c.segs) == 0 {
		return nil
	}
	origin := c.segs
	var want int64
	for _, p := range origin {
		want += int64(len(p))
	}
	n, err := c.segs.WriteTo(c.rwc)
	if err == nil && n != want {
		err = io.ErrShortWrite
	}
	c.segs = origin[:0]
	return c.recordWriteError(err)
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
