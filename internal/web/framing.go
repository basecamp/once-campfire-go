package web

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strconv"

	"github.com/basecamp/once-campfire-go/internal/fastserve"
)

// This file is the ENGINE-49 precomposed-framing path for recorded responses.
//
// A recorded response's header set is fixed per (route, message version,
// encoding): the security headers and Content-Type are process constants, the
// ETag is a pure function of the cache-stable pieces (already content-keyed in
// the piece cache), Cache-Control/Content-Encoding/Vary are per-encoding
// constants, and Content-Length is the sum of the chosen members. So instead
// of formatting an http.Header map per request (13 Set calls, the hex ETag
// string, the Vary join, the Content-Length string), the response head is
// precomputed every request by appending static and cached lines into the
// request arena, and handed to the owned writer (fastserve) as a byte block —
// zero per-request allocations.
//
// The variant cache stores the one per-variant string that is expensive to
// rebuild: the weak ETag header value. It is keyed by a digest of (shell
// identity, payload digest, encoding). The shell identity and the payload
// digest are the same content keys the pieces are stored under, so a variant
// hit is correct by construction: when the shell or the message list changes,
// their content keys change and the variant key misses. No invalidation list
// exists because nothing is invalidated by hand.
//
// The header block is emitted in the exact order the map path emits it
// (sort.Strings over the canonical keys, then the server extras), and the
// byte-parity test in recorded_pieces_test.go compares the on/off heads
// through the fastserve writer. The precomposed path is only taken when:
//
//   - CAMPFIRE_PRECOMPOSED_FRAMING is on (default) and the arena is on,
//   - the underlying writer chain ends in a writer that accepts a
//     precomposed block (fastserve.response),
//   - the response is a 200 with a known body, over HTTP/1.1,
//   - no Set-Cookie was added and the browser session is unchanged (a
//     session commit would have to reach the emitted head),
//   - the shell and payload variant is cached (the first request for a
//     variant fills the cache on the map path and serves the same bytes).
//
// Anything else takes the map path and is byte-identical to the pre-framing
// tree.

// precomposedReceiver is implemented by writers that accept a fully formed
// recorded response: the status line and Date are the receiver's own (the
// receiver caches them; web cannot know the writer's Date formatting), head
// is the header block in the map path's exact order without the trailing
// CRLF, and parts are the body segments (an assembled gzip/zstd member or the
// identity raws — concatenated by the receiver exactly as the map path's
// Write loop concatenates them). fastserve.response implements it.
type precomposedReceiver interface {
	// WritePrecomposed emits the response. body is the segment list (one
	// assembled member or the identity raws); the receiver concatenates it
	// exactly as the map path's Write loop would. It reports an error only
	// when a body segment write fails.
	WritePrecomposed(status int, head []byte, body [][]byte) error
}

// precomposedGate is implemented by a response policy that must be consulted
// before a response is framed: it answers whether a body carrying this
// Content-Encoding ("" for identity) would be transformed by the policy, in
// which case the map path must serve the response so the policy's own
// transformation runs. The public compression policy implements it
// (front.publicResponse.FramePrecomposed); a chain with no gate transforms
// nothing.
// The method is exported (like WritePrecomposed) because Go's interface
// satisfaction is structural across packages: an unexported method could only
// be implemented inside this package.
type precomposedGate interface {
	FramePrecomposed(contentEncoding string) bool
}

// precomposedFramingAllowed reports whether the chain lets a response with this
// wire encoding be framed. It is asked at the framing decision, where the
// response's actual encoding is known, rather than at the writer gate: the
// policy knows codings the application's selector does not (zstd), so only the
// response itself can say whether the policy would be a pass-through.
func precomposedFramingAllowed(w http.ResponseWriter, contentEncoding string) bool {
	for {
		if gate, ok := w.(precomposedGate); ok {
			return gate.FramePrecomposed(contentEncoding)
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return true
		}
		w = wrapper.Unwrap()
	}
}

// findPrecomposedReceiver walks the writer chain (the same walk
// findResponseBuffer uses) for a precomposedReceiver.
func findPrecomposedReceiver(w http.ResponseWriter) precomposedReceiver {
	for {
		if receiver, ok := w.(precomposedReceiver); ok {
			return receiver
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = wrapper.Unwrap()
	}
}

// variantKeyDigest hashes the variant key inputs in place: sha256 of the
// shell identity, the payload digest and the encoding byte. The pieces for
// shell and payload are content-keyed, so the variant key moves exactly when
// the response's stable content moves.
func variantKeyDigest(shellIdentity [32]byte, payload [32]byte, encoding byte) [32]byte {
	var input [65]byte
	copy(input[0:32], shellIdentity[:])
	copy(input[32:64], payload[:])
	input[64] = encoding
	return sha256.Sum256(input[:])
}

// encodeTag names the wire encoding byte used in a variant key, matching
// piececache.Encoding's identity/gzip/zstd order.
const (
	encodeIdentity byte = iota
	encodeGzip
	encodeZstd
)

// appendRecordedHead builds the recorded response's header block (the sorted
// handler-header lines, without the status line, Date and final CRLF — the
// receiver supplies those) into dst and returns the extended slice. The line
// order is the map path's sort.Strings order over the security headers,
// Content-Type, the recorded headers and Content-Length; the byte-parity test
// pins it against the map path through the fastserve writer. etag is the
// validator spelling (arena bytes on the framed path); xVersion/xRev are the
// process-constant values. length is the response body length for the chosen
// encoding. A 304 status retains the ETag and suppresses Content-Length and
// Content-Type like the map path's notModified and net/http's suppressedHeaders.
func (s *Server) appendRecordedHead(dst []byte, etag []byte, lastModified string, encoding string, length int, status int) []byte {
	if status == http.StatusNotModified {
		// The map path's 304 is composed at finish from the restored map:
		// the finish-time Cache-Control fallback ("no-cache"), the security
		// headers, selected ETag, Date and the close header. It keeps the
		// Content-Encoding (net/http's suppressedHeaders drops only
		// Content-Type, Content-Length and Transfer-Encoding), so the framed
		// 304 must keep it too or the two paths' 304s would differ.
		dst = append(dst, "Cache-Control: no-cache\r\n"...)
		if encoding != "" {
			dst = append(dst, "Content-Encoding: "...)
			dst = append(dst, encoding...)
			dst = append(dst, '\r', '\n')
		}
		dst = append(dst, "Etag: "...)
		dst = append(dst, etag...)
		dst = append(dst, '\r', '\n')
		dst = append(dst, "Referrer-Policy: strict-origin-when-cross-origin\r\n"...)
		return s.appendRecordedSecurity(dst)
	}
	dst = append(dst, "Cache-Control: max-age=0, private, must-revalidate\r\n"...)
	if encoding != "" {
		dst = append(dst, "Content-Encoding: "...)
		dst = append(dst, encoding...)
		dst = append(dst, '\r', '\n')
	}
	dst = append(dst, "Content-Length: "...)
	dst = strconv.AppendInt(dst, int64(length), 10)
	dst = append(dst, '\r', '\n')
	dst = append(dst, "Content-Type: text/html; charset=utf-8\r\n"...)
	dst = append(dst, "Etag: "...)
	dst = append(dst, etag...)
	dst = append(dst, '\r', '\n')
	// The messages page's validator adds Last-Modified; a recorded block for
	// a page that carries it must emit it in sorted position.
	if lastModified != "" {
		dst = append(dst, "Last-Modified: "...)
		dst = append(dst, lastModified...)
		dst = append(dst, '\r', '\n')
	}
	dst = append(dst, "Referrer-Policy: strict-origin-when-cross-origin\r\n"...)
	dst = append(dst, "Vary: Accept-Encoding\r\n"...)
	return s.appendRecordedSecurity(dst)
}

// appendRecordedSecurity appends the process-constant security headers every
// served page carries, sorted after the recorded headers. Referrer-Policy is
// emitted by the caller (before Vary), so the X- headers here start the
// remaining sorted run.
func (s *Server) appendRecordedSecurity(dst []byte) []byte {
	dst = append(dst, "X-Content-Type-Options: nosniff\r\n"...)
	dst = append(dst, "X-Frame-Options: SAMEORIGIN\r\n"...)
	dst = append(dst, "X-Permitted-Cross-Domain-Policies: none\r\n"...)
	dst = append(dst, "X-Rev: "...)
	dst = append(dst, s.xRev...)
	dst = append(dst, '\r', '\n')
	dst = append(dst, "X-Version: "...)
	dst = append(dst, s.xVersion...)
	dst = append(dst, '\r', '\n')
	return append(dst, "X-Xss-Protection: 0\r\n"...)
}

// restoreRecordedHeaders repopulates the header map with the fixed security
// headers and Content-Type a recorded response carries, for the rare request
// whose writer gate passed but that fell back to the map path (a changed
// browser session, a Set-Cookie, an arena that could not fit the body). The
// values are the same bytes ServeHTTP and render set on the non-framed path,
// so the fallback response is byte-identical to one that never framed.
func (s *Server) restoreRecordedHeaders(h http.Header) {
	s.restoreSecurityHeaders(h)
	h.Set("Content-Type", "text/html; charset=utf-8")
}

func (s *Server) restoreSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-XSS-Protection", "0")
	h.Set("X-Permitted-Cross-Domain-Policies", "none")
	h.Set("X-Version", s.xVersion)
	h.Set("X-Rev", s.xRev)
}

// precomposedEligible reports whether the request may take the precomposed
// path: HTTP/1.1, no Set-Cookie added so far, and an unchanged browser
// session (a session commit would have to reach the emitted head). state may
// be nil when no browser session exists (bench writers).
func precomposedEligible(r *http.Request, h http.Header, state *browserSession) bool {
	// The precomposed writer bypasses the transport's body-drain and
	// Expect decisions. Only bodyless requests can use this lane.
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Expect") != "" || r.Body != nil && r.Body != http.NoBody {
		return false
	}
	if !protoAtLeastHTTP11(r) {
		return false
	}
	if h.Get("Set-Cookie") != "" {
		return false
	}
	if state != nil && state.changed {
		return false
	}
	return true
}

// protoAtLeastHTTP11 reports whether the request is HTTP/1.1 or newer; the
// precomposed head mirrors the map path's keep-alive framing, which is only
// that simple at HTTP/1.1.
func protoAtLeastHTTP11(r *http.Request) bool {
	return r.ProtoMajor > 1 || r.ProtoMajor == 1 && r.ProtoMinor >= 1
}

// framedHead returns the head block the fresh response emitted through the
// precomposed lane, or nil when the response did not frame (the map path, a
// 304, or no recorded head). The whole-response cache uses it as the replay
// head's source: the block is exactly what reached the wire (plus the seam's
// spliced X-Cache marker, added by buildReplayHead).
func framedHead(w *responseBuffer) []byte {
	if w == nil || !w.precomposed || w.recordedStatus != http.StatusOK || len(w.recordedHead) == 0 {
		return nil
	}
	return w.recordedHead
}

// buildReplayHead renders the immutable 200 replay head block for the
// whole-response cache: the fresh response's exact header lines with
// Content-Length and the front's X-Cache marker included, right-sized so no
// seam can splice into the shared entry.
//
// framed is the head block the fresh response framed with (nil on the map
// path). On the map path the block is rendered from the live map, which is
// the same set of lines the loop's composeHead emits, in the same sorted
// order and with the same value sanitization (fastserve.AppendHeaderLines is
// the same pass). Content-Length is added because finish sets it after
// admission on that path.
//
// A map carrying a line the receiver owns or the framing extras would
// duplicate (Date, Transfer-Encoding, Connection, Trailer) has no replay
// head: the hit takes the map path, byte-identically. A Set-Cookie line is
// dropped instead: the head is the response shape a request with no cookie to
// set emits, and any request that must emit one carries it on the live map,
// where precomposedEligible refuses the replay.
func (s *Server) buildReplayHead(live http.Header, framed []byte, length int, xcache string) []byte {
	if framed == nil {
		for _, name := range [...]string{"Date", "Transfer-Encoding", "Connection", "Trailer"} {
			if _, ok := live[name]; ok {
				return nil
			}
		}
		h := make(http.Header, len(live)+1)
		for name, values := range live {
			if name == "Set-Cookie" {
				continue
			}
			h[name] = values
		}
		h.Set("Content-Length", strconv.Itoa(length))
		return rightSized(fastserve.AppendHeaderLines(nil, h, nil))
	}
	block := make([]byte, len(framed), len(framed))
	copy(block, framed)
	if xcache != "" {
		block = spliceHeadLine(block, "X-Cache", xcache)
	}
	return rightSized(block)
}

// buildReplay304 renders the immutable 304 replay block for the recorded
// flavor: the head lane's own 304 (Cache-Control: no-cache, the response's
// ETag, content coding and security headers), with the front's X-Cache marker
// baked in like the 200 block's. The validator flavor does not use a block —
// its fresh 304 carries no coding, so its hit takes the map path (see
// serveCached304) — and passes nil.
func (s *Server) buildReplay304(header http.Header, validator bool, xcache string) []byte {
	if validator {
		return nil
	}
	block := s.appendRecordedHead(nil, []byte(header.Get("ETag")), "", header.Get("Content-Encoding"), 0, http.StatusNotModified)
	if xcache != "" {
		block = spliceHeadLine(block, "X-Cache", xcache)
	}
	return rightSized(block)
}

// spliceHeadLine inserts one rendered "Name: value\r\n" line into a head block
// sorted by canonical header name and returns the extended block. It is used
// at admission (once per entry) to bake the front's X-Cache marker into a
// replay block, so the per-hit seam never has to splice — and never writes
// into the shared entry. The caller guarantees the line is absent.
func spliceHeadLine(head []byte, name, value string) []byte {
	var lineBuf [80]byte
	line := append(lineBuf[:0], name...)
	line = append(line, ':', ' ')
	line = append(line, value...)
	line = append(line, '\r', '\n')
	at := len(head)
	for i := 0; i < len(head); {
		end := bytes.IndexByte(head[i:], '\n')
		if end < 0 {
			break
		}
		end += i + 1
		if colon := bytes.IndexByte(head[i:end], ':'); colon >= 0 && bytes.Compare(head[i:i+colon], line[:len(name)]) > 0 {
			at = i
			break
		}
		i = end
	}
	grown := make([]byte, 0, len(head)+len(line))
	grown = append(grown, head[:at]...)
	grown = append(grown, line...)
	return append(grown, head[at:]...)
}

// rightSized returns b with cap == len. A replay block must be exactly sized:
// the front's seam inserts a missing line in place when the block has spare
// capacity, which would write into the shared cache entry.
func rightSized(b []byte) []byte {
	if cap(b) == len(b) {
		return b
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
