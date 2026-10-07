package web

import (
	"crypto/sha256"
	"net/http"
	"strconv"
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
// encoding. A 304 status suppresses the entity headers (Content-Encoding,
// Content-Length, Content-Type) exactly like the map path's notModified del
// and net/http's suppressedHeaders.
func (s *Server) appendRecordedHead(dst []byte, etag []byte, lastModified string, encoding string, length int, status int) []byte {
	if status == http.StatusNotModified {
		// The map path's 304 is composed at finish from the restored map:
		// the finish-time Cache-Control fallback ("no-cache"), the security
		// headers, Date and the close header — no ETag, Vary or entity
		// headers. Replicate that head exactly.
		dst = append(dst, "Cache-Control: no-cache\r\n"...)
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
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-XSS-Protection", "0")
	h.Set("X-Permitted-Cross-Domain-Policies", "none")
	h.Set("X-Version", s.xVersion)
	h.Set("X-Rev", s.xRev)
	h.Set("Content-Type", "text/html; charset=utf-8")
}

// precomposedEligible reports whether the request may take the precomposed
// path: HTTP/1.1, no Set-Cookie added so far, and an unchanged browser
// session (a session commit would have to reach the emitted head). state may
// be nil when no browser session exists (bench writers).
func precomposedEligible(r *http.Request, h http.Header, state *browserSession) bool {
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
