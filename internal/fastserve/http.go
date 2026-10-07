// Package fastserve is the owned HTTP/1.x server loop for the internal
// (target) listener and, since ENGINE-62, for the plain-HTTP public listener
// (TLS/ACME listeners stay on net/http). CAMPFIRE_SERVER_LOOP=off rolls both
// back to net/http.
//
// It replaces net/http's conn loop on that listener only: request heads are
// parsed by github.com/sebishogun/simdhttp/http1 (vector scans, zero
// allocations), request bodies use the framing table of the same package, and
// responses are serialized into per-connection scratch buffers and written
// with a single vectored write (net.Buffers / writev) per flush instead of
// net/http's status-line, header-block and body syscalls. All response
// semantics — auto Content-Length, chunked fallback, 100-continue, Date,
// sorted header order, keep-alive decisions, limits-to-status mapping — are
// implemented to match net/http byte for byte, because the loop is in front of
// the same application handler and must not change what the client sees.
//
// Connections whose parsed head contains an Upgrade header, and connections
// that open with the HTTP/2 preface, are handed off to an internal
// net/http.Server via a prefix-replaying net.Conn wrapper: the bytes this
// loop already consumed are replayed first, so hijacking (WebCable's
// WebSocket accept) and h2c keep working untouched through the standard
// server. Handed-off connections never run the application handler inside this
// package.
//
// Deliberate differences from net/http, all inherited from the simdhttp head
// parser and documented there: bare-LF line endings are rejected (400), a
// header name must be an RFC 9110 token (a space in a name is 400), duplicate
// Content-Length / Transfer-Encoding lines are rejected, a transfer encoding
// other than exactly "chunked" is 400 (net/http answers 501), and an empty
// Host counts as missing (400). Limit verdicts differ in one direction:
// simdhttp's Compatible profile caps the request line at 8 KiB and 100
// headers, so an over-long request line or very many headers yield 431 where
// net/http (64 KiB head cap on this listener) would still answer. The 413
// body-limit, timeout, keep-alive and chunked-request paths are net/http
// parity. The plan record for these deltas is plans/engine-41.md.
package fastserve

import (
	"net/http"
	"strings"
)

// hasToken reports whether v is a comma-separated list of tokens containing
// token, ASCII case-insensitive. Mirrors net/http's hasToken.
func hasToken(v, token string) bool {
	if len(token) > len(v) || token == "" {
		return false
	}
	for sp := 0; sp <= len(v)-len(token); sp++ {
		// Token is ASCII; skip positions where neither the byte nor its
		// uppercase counterpart matches the token's first byte.
		if v[sp] != token[0] && (v[sp]|0x20) != token[0] {
			continue
		}
		// The whole token must be one list element: leading edge is start
		// or comma + OWS, trailing edge is comma or end.
		if sp > 0 && !(v[sp-1] == ',' || v[sp-1] == ' ' || v[sp-1] == '\t') {
			continue
		}
		end := sp + len(token)
		if end < len(v) && !(v[end] == ',' || v[end] == ' ' || v[end] == '\t') {
			continue
		}
		if strings.EqualFold(v[sp:end], token) {
			return true
		}
	}
	return false
}

// shouldClose reports whether the request wants the connection closed after
// the response, the way net/http's shouldClose does for requests: HTTP/0.x
// closes; HTTP/1.0 closes unless keep-alive is requested; HTTP/1.1 closes on
// an explicit Connection: close.
func shouldClose(protoMajor, protoMinor int, header http.Header) bool {
	if protoMajor < 1 {
		return true
	}
	if protoMinor == 0 && !hasToken(header.Get("Connection"), "keep-alive") {
		return true
	}
	return hasToken(header.Get("Connection"), "close")
}

// wantsHTTP10KeepAlive reports whether an HTTP/1.0 request asked the server
// to keep the connection alive.
func wantsHTTP10KeepAlive(protoMajor, protoMinor int, header http.Header) bool {
	return protoMajor == 1 && protoMinor == 0 && hasToken(header.Get("Connection"), "keep-alive")
}

// fixPragmaCacheControl mirrors net/http's readRequest step: a request with
// "Pragma: no-cache" and no Cache-Control gets Cache-Control: no-cache.
func fixPragmaCacheControl(header http.Header) {
	if header.Get("Cache-Control") == "" && header.Get("Pragma") == "no-cache" {
		header.Set("Cache-Control", "no-cache")
	}
}

// expectsContinue reports whether the head asked for 100-continue, the way
// net/http's Request.expectsContinue does (token match on Expect).
func expectsContinue(header http.Header) bool {
	return hasToken(header.Get("Expect"), "100-continue")
}

// h2Preface is the HTTP/2 connection preface, as net/http sniffs it before
// readRequest (maybeServeUnencryptedHTTP2). The simdhttp parser rejects the
// preface's request line ("HTTP/2.0" is not an /1.x version), so the conn
// loop detects it from the buffered bytes and hands the connection off.
var h2Preface = []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

// isPrefacePrefix reports whether b is a prefix of the HTTP/2 preface. A
// client sending the preface in fragments must not be answered 400 on the
// first fragment, so the loop keeps reading while the buffer stays a strict
// prefix, and hands off once the full preface has arrived.
func isPrefacePrefix(b []byte) bool {
	if len(b) > len(h2Preface) {
		return false
	}
	return string(h2Preface[:len(b)]) == string(b)
}

// globalOptionsHandler replicates net/http's interception of "OPTIONS *"
// (serverHandler checks it before the application handler runs): 200,
// Content-Length: 0, and up to 4 KiB of request body read and discarded.
type globalOptionsHandler struct{}

func (globalOptionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")
	if r.ContentLength != 0 {
		mb := http.MaxBytesReader(w, r.Body, 4<<10)
		var buf [512]byte
		for {
			n, err := mb.Read(buf[:])
			if err != nil && n == 0 {
				return
			}
		}
	}
}
