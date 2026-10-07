package fastserve

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The response side replicates net/http's response/chunkWriter semantics so
// the application handler produces byte-identical responses over either loop.
// Every wire decision is annotated with the net/http block it mirrors; see
// plans/engine-41.md for the verification record.

const (
	// bufferBeforeChunkingSize mirrors net/http's constant: a response whose
	// body fits this budget is emitted once with an automatic
	// Content-Length; anything that overflows before the handler returns
	// switches to chunked.
	bufferBeforeChunkingSize = 2048
	// emissionSize coalesces fixed-length body writes into ~32 KiB segments
	// (bytes identical at any size; larger segments mean fewer syscalls).
	emissionSize = 32 << 10
	// maxPostHandlerReadBytes mirrors net/http: a request body the handler
	// left unread is drained up to this budget so the connection can be
	// reused; anything larger forces a close.
	maxPostHandlerReadBytes = 256 << 10
)

var (
	errBodyNotAllowed = errors.New("http: request method or response status code does not allow body")
	errContentLength  = errors.New("http: wrote more than the declared Content-Length")
)

// bodyAllowedForStatus mirrors net/http's transfer.go.
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == http.StatusNoContent, status == http.StatusNotModified:
		return false
	}
	return true
}

// BodyAllowedForStatus reports whether the status admits a response body, the
// way net/http's bodyAllowedForStatus does. The front cache's recorded-replay
// builder uses it to refuse a precomposed entry whose body the map path would
// have suppressed.
func BodyAllowedForStatus(status int) bool { return bodyAllowedForStatus(status) }

// suppressedHeaders mirrors net/http's transfer.go: 304 drops the entity
// headers; 204 (and every other bodyless status) drops CL and TE.
func suppressedHeaders(status int) []string {
	if status == http.StatusNotModified {
		return []string{"Content-Type", "Content-Length", "Transfer-Encoding"}
	}
	if !bodyAllowedForStatus(status) {
		return []string{"Content-Length", "Transfer-Encoding"}
	}
	return nil
}

// isProtocolSwitchResponse mirrors net/http's response.go: a 101 (or any
// response upgrading via Connection: Upgrade) never gets the auto close
// header.
func isProtocolSwitchResponse(code int, h http.Header) bool {
	return code == http.StatusSwitchingProtocols && h.Get("Upgrade") != "" &&
		hasToken(h.Get("Connection"), "Upgrade")
}

// excludedHeadersNoBody mirrors net/http: 1xx interim responses carry no
// entity headers.
var excludedHeadersNoBody = map[string]bool{"Content-Length": true, "Transfer-Encoding": true}

// response is the per-request ResponseWriter state. All of its byte storage
// lives on the owning conn and is reused across keep-alive requests.
type response struct {
	c   *conn
	req *http.Request

	handlerHeader   http.Header // the map Header() returns
	calledHeader    bool        // Header() was called
	headerSnapshot  http.Header // clone used for the wire (net/http snapshot)
	wroteHeader     bool
	status          int
	contentLength   int64 // -1 unknown; >= 0 known
	written         int64
	brokeConn       bool // write error or overrun: never reuse
	closeAfterReply bool
	handlerDone     bool

	// wantsClose and wants10KeepAlive are fixed at request build time, like
	// net/http's response (Issue 14940: the handler may mutate r.Header).
	wantsClose       bool
	wants10KeepAlive bool

	// requestBodyLimitHit mirrors net/http's flag of the same name: set by
	// requestTooLarge (http.MaxBytesReader's limit), it forces Connection:
	// close and the RST-avoidance close after the reply.
	requestBodyLimitHit bool

	// canWriteContinue mirrors net/http's atomic: 100-continue is sent by
	// the first body read unless a response started first.
	canWriteContinue bool
	// aborted marks a handler ending in a panic (or ErrAbortHandler):
	// nothing further is emitted and the connection closes, exactly as
	// net/http's conn.serve recover path leaves the wire.
	aborted bool

	// body accumulates writes; the head is composed on the first emission.
	// emitted is how much of body already reached the wire: emissions slice
	// body[emitted:] and advance the window instead of reslicing body, so
	// the array origin (and its full capacity) survives to the next request.
	body    []byte
	emitted int
	cw      chunkWriter
}

// chunkWriter composes the status line and header block once (mirroring
// net/http's chunkWriter.writeHeader) and frames the body from then on.
type chunkWriter struct {
	res         *response
	wroteHeader bool
	chunking    bool
}

// Header implements http.ResponseWriter.
func (w *response) Header() http.Header {
	w.calledHeader = true
	return w.handlerHeader
}

// WriteHeader implements http.ResponseWriter. The first call wins; 1xx codes
// other than 101 are emitted as interim responses immediately, like net/http.
func (w *response) WriteHeader(code int) {
	if code < 100 || code > 999 {
		panic("invalid WriteHeader code " + strconv.Itoa(code))
	}
	if w.wroteHeader {
		return // net/http logs a superfluous-call warning; the wire is unchanged
	}
	if code < 101 || code > 199 {
		// Any real response disables the automatically-sent 100 Continue.
		w.canWriteContinue = false
	}
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		b := w.c.composeHead(w.statusLine(code), w.handlerHeader, excludedHeadersNoBody, nil)
		w.c.rawWrite(b)
		return
	}
	w.wroteHeader = true
	w.status = code
	if w.calledHeader && w.headerSnapshot == nil {
		w.headerSnapshot = w.handlerHeader.Clone()
	}
	if cl := w.handlerHeader.Get("Content-Length"); cl != "" {
		if v, err := strconv.ParseInt(cl, 10, 64); err == nil && v >= 0 {
			w.contentLength = v
		} else {
			w.handlerHeader.Del("Content-Length") // net/http logs the invalid CL
		}
	}
}

// Write implements http.ResponseWriter.
func (w *response) Write(p []byte) (int, error) {
	if w.canWriteContinue {
		w.canWriteContinue = false
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !bodyAllowedForStatus(w.status) {
		return 0, errBodyNotAllowed
	}
	w.written += int64(len(p))
	if w.contentLength != -1 && w.written > w.contentLength {
		w.brokeConn = true
		return 0, errContentLength
	}
	w.body = append(w.body, p...)
	w.emitIfFull()
	return len(p), nil
}

// WriteString keeps io.WriteString callers on the direct path: the string
// appends straight into the body buffer, where net/http's bufio layer would
// absorb it the same way (no intermediate []byte conversion).
func (w *response) WriteString(s string) (int, error) {
	if w.canWriteContinue {
		w.canWriteContinue = false
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if len(s) == 0 {
		return 0, nil
	}
	if !bodyAllowedForStatus(w.status) {
		return 0, errBodyNotAllowed
	}
	w.written += int64(len(s))
	if w.contentLength != -1 && w.written > w.contentLength {
		w.brokeConn = true
		return 0, errContentLength
	}
	w.body = append(w.body, s...)
	w.emitIfFull()
	return len(s), nil
}

// emitIfFull mirrors the 2048-body budget: once the accumulation crosses it,
// the response is on the wire and, absent a declared Content-Length, in
// chunked framing. With a declared length the body coalesces into ~32 KiB
// wire segments (bytes identical at any size); chunked responses emit the
// whole pending window as one chunk, net/http's write-level granularity, so
// a small response costs one chunk flush plus the zero chunk.
func (w *response) emitIfFull() {
	for len(w.body)-w.emitted >= bufferBeforeChunkingSize {
		n := len(w.body) - w.emitted
		if w.contentLength != -1 {
			n = min(emissionSize, n)
		}
		if err := w.cw.writeBody(w.body[w.emitted : w.emitted+n]); err != nil {
			w.brokeConn = true
			return
		}
		w.emitted += n
		if w.emitted == len(w.body) {
			// The flush consumed everything; reset to the array origin so
			// the next append (and the connection, after the response)
			// keeps the full backing array rather than a tail subslice.
			w.body = w.body[:0]
			w.emitted = 0
		}
	}
}

// Flush implements http.Flusher: everything buffered goes to the wire, and
// the response becomes chunked from here on (net/http parity).
func (w *response) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	for len(w.body)-w.emitted > 0 {
		n := min(emissionSize, len(w.body)-w.emitted) // the header is composed on this emission
		if err := w.cw.writeBody(w.body[w.emitted : w.emitted+n]); err != nil {
			w.brokeConn = true
			return
		}
		w.emitted += n
	}
	// Everything pending reached the wire synchronously; reset to the array
	// origin so later writes (and the connection, after the response) keep
	// the full backing array, with the window aligned to the fresh body.
	w.c.bodyBuf = w.body
	w.body = w.body[:0]
	w.emitted = 0
	w.c.flushOut()
}

// finish runs after the handler returns: emit the composed response, close
// the chunked framing, and release the request body.
func (w *response) finish() {
	w.handlerDone = true
	if w.aborted {
		// Panicked handler: net/http closes the connection without
		// completing the response; whatever reached the wire stays.
		if w.req.Body != nil {
			w.req.Body.Close()
		}
		return
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if len(w.body)-w.emitted > 0 && !w.brokeConn {
		if err := w.cw.writeBody(w.body[w.emitted:]); err != nil {
			w.brokeConn = true
		}
	}
	w.c.bodyBuf = w.body // the origin slice: full backing array for the next request
	w.body = nil
	w.cw.close()
	w.c.flushOut()
	// Close the request body regardless of reuse (net/http parity); the
	// drain already consumed it in the common case, so this is idempotent.
	if w.req.Body != nil {
		w.req.Body.Close()
	}
}

// nowDate returns the RFC 1123 formatted current time, formatted at most
// once per second per connection (ENGINE-49): the bytes are identical to a
// per-response format within the same second, and the value aliases the
// per-connection date scratch for the request's synchronous emission.
func (c *conn) nowDate(now time.Time) []byte {
	if now.Unix() != c.dateSecond {
		c.dateSecond = now.Unix()
		c.dateLine = now.UTC().AppendFormat(c.dateScratch[:0], http.TimeFormat)
	}
	return c.dateLine
}

// WritePrecomposed implements the web layer's precomposed-framing seam
// (internal/web/framing.go, ENGINE-49): the application hands the loop a
// fully formed recorded response — status, the header block in the map path's
// exact sorted order (Content-Length included), and the body parts — and the
// loop emits status line, Date and block itself, skipping the header-map
// composition entirely. The emitted bytes are identical to the map path's
// composeHead output (status + sorted headers + Date extra + CRLF), which the
// web layer's byte-parity tests pin. The receiver is safe only for responses
// the handler has fully finished (never streaming): the body length is
// declared from the parts, written is set to match, and the response is never
// chunked on this path.
func (w *response) WritePrecomposed(status int, head []byte, body [][]byte) error {
	if w.wroteHeader {
		return nil
	}
	w.wroteHeader = true
	w.cw.wroteHeader = true
	w.status = status
	total := 0
	for _, part := range body {
		total += len(part)
	}
	w.contentLength = int64(total)
	w.written = int64(total)
	c := w.c
	h := c.headBuf[:0]
	h = append(h, w.statusLine(status)...)
	h = append(h, head...)
	h = append(h, "Date: "...)
	h = append(h, c.nowDate(time.Now())...)
	h = append(h, '\r', '\n')
	// Mirror the map path's close decision: a request that asked for
	// Connection: close (or wants the connection closed), or a response
	// served while Shutdown is in progress, gets the close header in the
	// same extras position (after Date).
	closeConn := w.wantsClose || w.req.Header.Get("Connection") == "close" || c.srv.shuttingDown()
	if closeConn {
		w.closeAfterReply = true
		h = append(h, "Connection: close\r\n"...)
	}
	h = append(h, '\r', '\n') // the blank line ending the head
	c.emit(h)
	if w.req.Method != "HEAD" {
		for _, part := range body {
			if len(part) > 0 {
				c.emit(part)
			}
		}
	}
	err := c.flushOut()
	// The emission consumed h; keep its backing array for the next response.
	c.headBuf = h[:0]
	return err
}

// WriteRecorded emits a fully recorded response: head is the header block
// rendered by AppendHeaderLines (sorted header lines; no status line, Date or
// framing extras) and body is the complete recorded body. The receiver makes
// the same framing decisions the single-write map path makes for a completed
// handler response:
//
//   - HEAD and bodyless statuses emit the head only, with no Content-Length
//     and no Transfer-Encoding (net/http's auto-Content-Length gate);
//   - a body of at most bufferBeforeChunkingSize is emitted once with an
//     automatic Content-Length;
//   - a larger body is framed as one chunk plus the terminating zero chunk
//     (the front cache only records HTTP/1.1 responses).
//
// The head, Date and body are emitted as one writev. The close decision
// mirrors the map path: the request's close flag, a Connection: close request
// header, or an in-progress Shutdown append Connection: close. The caller must
// only use this for requests with no unread body: the map path's drain and
// 100-continue rules do not run here (the front cache lane restricts the
// recorded path to ContentLength 0 with no Transfer-Encoding). The head block
// must not carry Date, Content-Length, Transfer-Encoding, Trailer or
// Connection lines — the front's builder refuses such entries and falls back
// to the map path.
func (w *response) WriteRecorded(status int, head []byte, body []byte) error {
	if w.wroteHeader {
		return nil
	}
	w.wroteHeader = true
	w.cw.wroteHeader = true
	w.status = status
	c := w.c
	h := c.headBuf[:0]
	h = append(h, w.statusLine(status)...)
	h = append(h, head...)
	h = append(h, "Date: "...)
	h = append(h, c.nowDate(time.Now())...)
	h = append(h, '\r', '\n')
	headOnly := w.req.Method == "HEAD" || !bodyAllowedForStatus(status)
	chunked := false
	if !headOnly {
		if len(body) <= bufferBeforeChunkingSize {
			w.contentLength = int64(len(body))
			w.written = int64(len(body))
			h = append(h, "Content-Length: "...)
			h = strconv.AppendInt(h, int64(len(body)), 10)
			h = append(h, '\r', '\n')
		} else {
			chunked = true
			w.contentLength = -1
			w.written = int64(len(body))
		}
	}
	closeConn := w.wantsClose || w.req.Header.Get("Connection") == "close" || c.srv.shuttingDown()
	if closeConn {
		w.closeAfterReply = true
		h = append(h, "Connection: close\r\n"...)
	}
	if chunked {
		h = append(h, "Transfer-Encoding: chunked\r\n"...)
	}
	h = append(h, '\r', '\n')
	c.emit(h)
	if !headOnly {
		if chunked {
			chunk := strconv.AppendInt(c.chunkScratch[:0], int64(len(body)), 16)
			c.emit(append(chunk, '\r', '\n'))
			c.emit(body)
			c.emit(crlf)
			c.emit(zeroChunk)
		} else if len(body) > 0 {
			c.emit(body)
		}
	}
	err := c.flushOut()
	if err != nil {
		w.brokeConn = true
	}
	c.headBuf = h[:0]
	return err
}

// writeBody emits one body segment. The header block is composed on the
// first emission, carrying the first body bytes (sniffing, auto Content-
// Length decisions).
func (cw *chunkWriter) writeBody(p []byte) error {
	w := cw.res
	if !cw.wroteHeader {
		cw.writeHeader(p)
	}
	if w.req.Method == "HEAD" {
		return w.c.flushOut()
	}
	if cw.chunking {
		cw.writeChunk(p)
		return w.c.flushOut()
	}
	w.c.emit(p)
	return w.c.flushOut()
}

// writeChunk frames one chunk: hex size, CRLF, data, CRLF.
func (cw *chunkWriter) writeChunk(p []byte) {
	w := cw.res
	head := strconv.AppendInt(w.c.chunkScratch[:0], int64(len(p)), 16)
	w.c.emit(append(head, '\r', '\n')) // escapes into the scratch
	w.c.emit(p)
	w.c.emit(crlf)
}

// close finalizes the response: compose the header if nothing was emitted
// yet, then end the chunked framing with the zero chunk.
func (cw *chunkWriter) close() {
	w := cw.res
	if !cw.wroteHeader {
		cw.writeHeader(nil)
	}
	if cw.chunking {
		w.c.emit(zeroChunk)
	}
}

var zeroChunk = []byte("0\r\n\r\n")

// eheader carries the server-generated headers written after the sorted map
// (net/http's extraHeader, in the same order).
type eheader struct {
	date, contentLength, contentType, connection, transferEncoding []byte
}

// writeHeader duplicates net/http chunkWriter.writeHeader's decision table.
// p is the first body bytes, used for sniffing and the automatic
// Content-Length when the handler has already returned.
func (cw *chunkWriter) writeHeader(p []byte) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	w := cw.res

	// keepAlives mirrors net/http's server.doKeepAlives: once Shutdown has
	// started, every response is emitted with Connection: close.
	keepAlives := !w.c.srv.shuttingDown()

	header := w.wireHeader()
	var set eheader
	// delHeader removes from the snapshot (owned) or excludes from the live
	// map, exactly net/http's two paths.
	var exclude map[string]bool
	delHeader := func(key string) {
		if w.headerSnapshot != nil {
			w.headerSnapshot.Del(key)
			return
		}
		if _, ok := header[key]; !ok {
			return
		}
		if exclude == nil {
			exclude = make(map[string]bool, 2)
		}
		exclude[key] = true
	}

	te := header.Get("Transfer-Encoding")
	hasTE := te != ""

	// Automatic Content-Length: the handler finished, the status allows a
	// body, no CL was declared and no Transfer-Encoding was set (net/http's
	// !trailers && !hasTE: a TE-carrying response must not also declare a
	// length; trailer support is a documented delta), and this is not an
	// empty HEAD write.
	if w.handlerDone && bodyAllowedForStatus(w.status) && !hasTE && !headerHas(header, "Content-Length") && (w.req.Method != "HEAD" || len(p) > 0) {
		w.contentLength = int64(len(p))
		set.contentLength = strconv.AppendInt(w.c.lenScratch[:0], int64(len(p)), 10)
	}

	if w.wants10KeepAlive && keepAlives && headerHas(header, "Content-Length") && header.Get("Connection") == "keep-alive" {
		w.closeAfterReply = false
	}
	hasCL := w.contentLength != -1
	if w.wants10KeepAlive && (w.req.Method == "HEAD" || hasCL || !bodyAllowedForStatus(w.status)) {
		if !headerHas(header, "Connection") {
			set.connection = []byte("keep-alive")
		}
	} else if !protoAtLeast(w.req.ProtoMajor, w.req.ProtoMinor, 1, 1) || w.wantsClose {
		w.closeAfterReply = true
	}
	if header.Get("Connection") == "close" || !keepAlives {
		w.closeAfterReply = true
	}

	// A 100-continue body never read to EOF never reuses the connection,
	// even when the drain consumed it (net/http parity).
	if ecr, ok := w.req.Body.(*continueReader); ok && !ecr.sawEOF {
		w.closeAfterReply = true
	}

	// Drain the unread request body before the first response byte: this is
	// what lets a connection survive a handler that did not read the body,
	// and a body past the budget forces the close with a Connection: close
	// header, exactly as net/http writes it. requestTooLarge also marks the
	// limit hit so the loop finishes the connection with the RST-avoidance
	// close (net/http's closeWriteAndWait).
	if w.req.ContentLength != 0 && !w.closeAfterReply {
		switch w.drainRequestBody() {
		case drainTooBig:
			// net/http's budget overflow: requestTooLarge at drain time
			// (the header is implicitly written by now, so no map Set),
			// then the Connection is rewritten as a server extra header.
			w.requestTooLarge()
			delHeader("Connection")
			set.connection = []byte("close")
		case drainErr:
			// The body reader failed mid-drain: usually the app's
			// http.MaxBytesReader tripping on the remaining bytes. net/http
			// learns the same overflow from its maxBytesReader's direct
			// requestTooLarge call (an unexported-method interface this
			// loop cannot receive across the package boundary), whose
			// Connection: close lands in the header map when the overflow
			// preceded the first header write — the shape the app's
			// read-then-respond handlers produce. Place it there so the
			// sorted block matches net/http's bytes.
			w.requestTooLarge()
			if !headerHas(header, "Connection") {
				header.Set("Connection", "close")
			}
		}
	}

	code := w.status
	bodyAllowed := bodyAllowedForStatus(code)
	if bodyAllowed {
		if !headerHas(header, "Content-Type") && header.Get("Content-Encoding") == "" && !hasTE && len(p) > 0 {
			set.contentType = []byte(http.DetectContentType(p))
		}
	} else {
		for _, k := range suppressedHeaders(code) {
			delHeader(k)
		}
	}
	if !headerHas(header, "Date") {
		set.date = w.c.nowDate(time.Now())
	}

	// The framing decision, mirroring net/http's TE/CL table.
	if hasCL && hasTE && te != "identity" {
		delHeader("Content-Length")
		hasCL = false
	}
	if w.req.Method == "HEAD" || !bodyAllowed || code == http.StatusNoContent {
		delHeader("Transfer-Encoding")
	} else if hasCL {
		delHeader("Transfer-Encoding")
	} else if protoAtLeast(w.req.ProtoMajor, w.req.ProtoMinor, 1, 1) {
		if hasTE && te == "identity" {
			cw.chunking = false
			w.closeAfterReply = true
			delHeader("Transfer-Encoding")
		} else {
			cw.chunking = true
			set.transferEncoding = []byte("chunked")
			if hasTE && te == "chunked" {
				delHeader("Transfer-Encoding")
			}
		}
	} else {
		w.closeAfterReply = true
		delHeader("Transfer-Encoding")
	}
	if cw.chunking {
		delHeader("Content-Length")
	}
	if !protoAtLeast(w.req.ProtoMajor, w.req.ProtoMinor, 1, 0) {
		return
	}

	// Only a Connection the server itself decided is overridden; a protocol
	// switch response never gets the close header.
	if w.closeAfterReply && !hasToken(header.Get("Connection"), "close") && !isProtocolSwitchResponse(w.status, header) {
		delHeader("Connection")
		if protoAtLeast(w.req.ProtoMajor, w.req.ProtoMinor, 1, 1) {
			set.connection = []byte("close")
		}
	}

	w.c.emit(w.c.composeHead(w.statusLine(code), header, exclude, &set))
}

// statusLineCache holds pre-rendered status lines for HTTP/1.0 and HTTP/1.1
// (ENGINE-49): the same bytes the per-response formatter produced, formatted
// once at init so a warm response does no AppendInt work. Only codes with
// a standard StatusText are pre-rendered; unknown codes keep the
// per-response fallback in statusLine.
var statusLineCache [2][600][]byte

func init() {
	for code := 100; code < 600; code++ {
		if text := http.StatusText(code); text != "" {
			statusLineCache[0][code] = []byte("HTTP/1.0 " + strconv.Itoa(code) + " " + text + "\r\n")
			statusLineCache[1][code] = []byte("HTTP/1.1 " + strconv.Itoa(code) + " " + text + "\r\n")
		}
	}
}

// statusLine renders "HTTP/1.1 200 OK\r\n" (or HTTP/1.0), mirroring
// net/http's writeStatusLine including the unknown-code spelling. Standard
// codes come from the static cache; the fallback keeps the scratch formatter
// for the codes StatusText does not name.
func (w *response) statusLine(code int) []byte {
	if code >= 100 && code < 600 {
		proto11 := protoAtLeast(w.req.ProtoMajor, w.req.ProtoMinor, 1, 1)
		var line []byte
		if proto11 {
			line = statusLineCache[1][code]
		} else {
			line = statusLineCache[0][code]
		}
		if line != nil {
			return line
		}
	}
	s := w.c.statusScratch[:0]
	if protoAtLeast(w.req.ProtoMajor, w.req.ProtoMinor, 1, 1) {
		s = append(s, "HTTP/1.1 "...)
	} else {
		s = append(s, "HTTP/1.0 "...)
	}
	if text := http.StatusText(code); text != "" {
		s = strconv.AppendInt(s, int64(code), 10)
		s = append(s, ' ')
		s = append(s, text...)
		s = append(s, '\r', '\n')
	} else {
		s = strconv.AppendInt(s, int64(code), 10)
		s = append(s, " status code "...)
		s = strconv.AppendInt(s, int64(code), 10)
		s = append(s, '\r', '\n')
	}
	return s
}

// wireHeader returns the map that reaches the wire: the snapshot taken at
// WriteHeader when Header() was called, otherwise the live map.
func (w *response) wireHeader() http.Header {
	if w.headerSnapshot != nil {
		return w.headerSnapshot
	}
	return w.handlerHeader
}

func headerHas(h http.Header, key string) bool {
	_, ok := h[key]
	return ok
}

// requestTooLarge is what http.MaxBytesReader calls when the request body
// passes its limit (net/http's method of the same name): the connection never
// comes back, and the header, if not yet written, carries Connection: close
// so the client sees the verdict before the reset.
func (w *response) requestTooLarge() {
	w.closeAfterReply = true
	w.requestBodyLimitHit = true
	if !w.wroteHeader {
		w.handlerHeader.Set("Connection", "close")
	}
}

// drainRequestBody consumes an unread request body up to the net/http budget,
// reporting why the connection must close: drainTooBig when the body exceeded
// the post-handler budget and drainErr when the body reader itself failed (a
// mid-body protocol error, or http.MaxBytesReader tripping during the drain).
// The 100-continue wrapper is bypassed, exactly as net/http's drain reads the
// raw body: draining must not trigger the interim response.
type drainVerdict int

const (
	drainOK drainVerdict = iota
	drainTooBig
	drainErr
)

func (w *response) drainRequestBody() drainVerdict {
	scratch := w.c.drainScratch[:]
	body := w.req.Body
	if cw, ok := body.(*continueReader); ok {
		body = cw.ReadCloser
	}
	var read int64
	for {
		n, err := body.Read(scratch)
		read += int64(n)
		if read > maxPostHandlerReadBytes {
			return drainTooBig
		}
		if err == io.EOF {
			if closer, ok := body.(io.Closer); ok {
				closer.Close()
			}
			return drainOK
		}
		if err != nil {
			return drainErr
		}
	}
}

// shouldKeepAlive is net/http's shouldReuseConnection: the close flag, a
// declared Content-Length the handler under-fulfilled, or a broken write all
// end the connection.
func (w *response) shouldKeepAlive() bool {
	if w.closeAfterReply || w.brokeConn {
		return false
	}
	if w.req.Method != "HEAD" && w.contentLength != -1 && bodyAllowedForStatus(w.status) && w.contentLength != w.written {
		return false
	}
	return true
}

// composeHead renders status line + sorted header map + server-generated
// headers + CRLF into the per-conn head buffer and returns it as one wire
// segment. Values are sanitized the way net/http's writeSubset does: each
// value on its own line, newlines replaced by spaces, OWS trimmed, invalid
// field names dropped.
func (c *conn) composeHead(status []byte, h http.Header, exclude map[string]bool, set *eheader) []byte {
	head := c.headBuf[:0]
	head = append(head, status...)
	head, keys := appendHeaderLines(head, c.sortKeys[:0], h, exclude)
	// extraHeader order: Date, Content-Length, Content-Type, Connection,
	// Transfer-Encoding.
	if set != nil {
		if set.date != nil {
			head = append(head, "Date: "...)
			head = append(head, set.date...)
			head = append(head, '\r', '\n')
		}
		if set.contentLength != nil {
			head = append(head, "Content-Length: "...)
			head = append(head, set.contentLength...)
			head = append(head, '\r', '\n')
		}
		for _, x := range []struct {
			name  string
			value []byte
		}{{"Content-Type", set.contentType}, {"Connection", set.connection}, {"Transfer-Encoding", set.transferEncoding}} {
			if x.value != nil {
				head = append(head, x.name...)
				head = append(head, ": "...)
				head = append(head, x.value...)
				head = append(head, '\r', '\n')
			}
		}
	}
	head = append(head, '\r', '\n')
	// Keep the grown backing arrays on the connection for the next response.
	// The returned head is consumed by the flush that follows, and the key
	// scratch only by this composition, so both can be reused next request.
	c.headBuf = head[:0]
	c.sortKeys = keys[:0]
	return head
}

// appendHeaderLines appends h's header lines into dst in the exact order and
// spelling composeHead writes: keys sorted (StringOrder), invalid field names
// dropped silently, one line per value, values sanitized the way net/http's
// writeSubset does. keys is caller scratch, returned reset for reuse; exclude
// drops keys entirely (the map path's delHeader). No status line, Date or
// other server extras are included.
func appendHeaderLines(dst []byte, keys []string, h http.Header, exclude map[string]bool) ([]byte, []string) {
	keys = keys[:0]
	for k := range h {
		if exclude[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !validHeaderName(k) {
			continue // net/http drops invalid names silently
		}
		for _, v := range h[k] {
			dst = append(dst, k...)
			dst = append(dst, ':', ' ')
			// Values are written as strings: append([]{byte}, string...)
			// copies in place without an intermediate []byte allocation,
			// and the common no-transform case of the sanitizer returns
			// its input unchanged (net/http's writeSubset writes the
			// string the same way).
			dst = append(dst, sanitizeHeaderValue(v)...)
			dst = append(dst, '\r', '\n')
		}
	}
	return dst, keys
}

// AppendHeaderLines renders h into dst exactly as the wire would (the
// composeHead line pass, minus the status line and server extras) and returns
// the extended slice. The front cache uses it to build the head block of a
// recorded replay once per cache entry; the block is handed back through
// response.WriteRecorded, which appends the status line, Date and the framing
// extras. Rendering is done at entry-creation time, so the per-request replay
// path does no map, sort or sanitize work.
func AppendHeaderLines(dst []byte, h http.Header, exclude map[string]bool) []byte {
	keys := make([]string, 0, len(h))
	dst, _ = appendHeaderLines(dst, keys, h, exclude)
	return dst
}

// sanitizeHeaderValue mirrors net/http's headerNewlineToSpace and
// textproto.TrimString applied to each wire value. When nothing needs
// changing the input string is returned as is, so the per-value write costs
// no allocation.
func sanitizeHeaderValue(v string) string {
	if !strings.ContainsAny(v, "\r\n") {
		return strings.Trim(v, " \t")
	}
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\r", " "), "\n", " ")
	return strings.Trim(v, " \t")
}

// validHeaderName reports whether a response header name is an RFC 9110
// token; net/http drops invalid names silently on the wire.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		}
		return false
	}
	return true
}

var crlf = []byte("\r\n")
