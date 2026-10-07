package fastserve

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"net/url"

	"github.com/sebishogun/simdhttp/http1"
)

// protoAtLeast mirrors http.Request's ProtoAtLeast for the wire logic.
func protoAtLeast(protoMajor, protoMinor, major, minor int) bool {
	return protoMajor > major || protoMajor == major && protoMinor >= minor
}

// newRequest assembles a net/http *Request from a simdhttp-parsed head, with
// the fields net/http's readRequest produces for the same bytes: the Host
// header validated and removed from the map, a valid single Content-Length
// kept in the map (net/http keeps it), Transfer-Encoding moved to the
// TransferEncoding field, Pragma folded into Cache-Control, and the body
// served by an http1.BodyReader over the connection's persistent buffered
// reader (so pipelined bytes stay inside the reader for the next request).
//
// ctx is the request's context; the caller owns its cancellation, exactly as
// conn.readRequest owns the per-request WithCancel context in net/http.
//
// The body framing follows http1.FramingOf, the single framing decision, so a
// head this parser accepted frames exactly the way net/http would.
func (c *conn) newRequest(parsed *http1.Request, br *bufio.Reader, ctx context.Context) (*http.Request, error) {
	// One allocation for the Request: WithContext copies the zero struct, so
	// the ctx is set without a second clone.
	req := (&http.Request{}).WithContext(ctx)
	req.Method = string(parsed.Method)
	req.RequestURI = string(parsed.Target)
	// The parser admits only HTTP/1.0 and HTTP/1.1 heads (anything else is
	// malformed), and net/http's req.Proto for an accepted request is the
	// wire text — exactly one of these two constants, so no copy is needed.
	if parsed.Proto[7] == '0' {
		req.Proto = "HTTP/1.0"
	} else {
		req.Proto = "HTTP/1.1"
	}
	req.ProtoMajor, req.ProtoMinor = 1, int(parsed.Proto[7]-'0')

	// The request-target forms net/http accepts: origin-form, asterisk-form,
	// and CONNECT authority-form. Any other target fails url.ParseRequestURI,
	// exactly as net/http's readRequest does ("malformed HTTP request" 400).
	rawurl := req.RequestURI
	justAuthority := req.Method == "CONNECT" && !hasPrefix(rawurl, "/")
	if justAuthority {
		rawurl = "http://" + rawurl
	}
	u, err := url.ParseRequestURI(rawurl)
	if err != nil {
		return nil, err
	}
	if justAuthority {
		u.Scheme = ""
	}
	req.URL = u

	// Header map with canonical keys; duplicate lines for the same name fold
	// into one key's value slice, like textproto.ReadMIMEHeader.
	header := make(http.Header, len(parsed.Headers))
	for i := range parsed.Headers {
		h := &parsed.Headers[i]
		key := canonicalHeaderKey(h.Name)
		header[key] = append(header[key], string(h.Value))
	}
	if len(header["Host"]) > 1 {
		return nil, errors.New("fastserve: too many Host headers")
	}
	req.Host = req.URL.Host
	if req.Host == "" {
		req.Host = header.Get("Host")
	}
	fixPragmaCacheControl(header)
	req.Close = shouldClose(req.ProtoMajor, req.ProtoMinor, header)

	framing, err := http1.FramingOf(parsed.ContentLengthLines, parsed.TransferEncodingLines, http1.Compatible)
	if err != nil {
		return nil, err // a framing verdict maps to plain 400; see package comment
	}
	switch framing.Kind {
	case http1.KindChunked:
		// parseTransferEncoding + fixLength parity: the Transfer-Encoding
		// header leaves the map, the field carries the coding,
		// ContentLength is -1, and any Content-Length is deleted.
		delete(header, "Transfer-Encoding")
		delete(header, "Content-Length")
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
	case http1.KindFixed:
		req.ContentLength = framing.Length
	}
	delete(header, "Host")
	req.RemoteAddr = c.remoteAddr
	req.TLS = nil
	req.Header = header

	if framing.Kind == http1.KindNone {
		req.Body = http.NoBody
		return req, nil
	}
	req.Body = http1.NewBodyReader(br, framing, c.srv.bodyLimits())
	return req, nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// canonicalHeaderKey returns the canonical spelling of a request header name
// without textproto's per-byte validation pass and allocation for the names
// the application actually sees (ENGINE-62: the public listener's hot paths
// carry the same dozen names). eqFold compares ASCII case-insensitively (the
// parser guarantees token bytes); anything unknown falls back to
// textproto.CanonicalMIMEHeaderKey, whose output is identical.
func canonicalHeaderKey(name []byte) string {
	switch len(name) {
	case 4:
		if eqFold(name, "host") {
			return "Host"
		}
		if eqFold(name, "date") {
			return "Date"
		}
	case 5:
		if eqFold(name, "range") {
			return "Range"
		}
	case 6:
		if eqFold(name, "accept") {
			return "Accept"
		}
		if eqFold(name, "cookie") {
			return "Cookie"
		}
		if eqFold(name, "expect") {
			return "Expect"
		}
		if eqFold(name, "origin") {
			return "Origin"
		}
		if eqFold(name, "pragma") {
			return "Pragma"
		}
	case 7:
		if eqFold(name, "referer") {
			return "Referer"
		}
		if eqFold(name, "upgrade") {
			return "Upgrade"
		}
	case 9:
		if eqFold(name, "forwarded") {
			return "Forwarded"
		}
	case 10:
		if eqFold(name, "connection") {
			return "Connection"
		}
		if eqFold(name, "user-agent") {
			return "User-Agent"
		}
	case 12:
		if eqFold(name, "content-type") {
			return "Content-Type"
		}
		if eqFold(name, "x-csrf-token") {
			return "X-Csrf-Token" // textproto's canonical spelling
		}
	case 13:
		if eqFold(name, "cache-control") {
			return "Cache-Control"
		}
		if eqFold(name, "authorization") {
			return "Authorization"
		}
		if eqFold(name, "if-none-match") {
			return "If-None-Match"
		}
	case 14:
		if eqFold(name, "content-length") {
			return "Content-Length"
		}
	case 15:
		if eqFold(name, "accept-encoding") {
			return "Accept-Encoding"
		}
		if eqFold(name, "x-forwarded-for") {
			return "X-Forwarded-For"
		}
		if eqFold(name, "x-request-start") {
			return "X-Request-Start"
		}
	case 16:
		if eqFold(name, "x-forwarded-host") {
			return "X-Forwarded-Host"
		}
		if eqFold(name, "x-forwarded-port") {
			return "X-Forwarded-Port"
		}
	case 17:
		if eqFold(name, "transfer-encoding") {
			return "Transfer-Encoding"
		}
		if eqFold(name, "if-modified-since") {
			return "If-Modified-Since"
		}
		if eqFold(name, "x-forwarded-proto") {
			return "X-Forwarded-Proto"
		}
	}
	return textproto.CanonicalMIMEHeaderKey(string(name))
}

// eqFold reports whether b equals lower, ASCII case-insensitively, assuming
// both are the same length and b contains only header token bytes.
func eqFold(b []byte, lower string) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// continueReader sends the 100-continue interim response before the first
// body read unless the response has started, mirroring net/http's
// expectContinueReader. sawEOF is what the connection-reuse decision checks:
// net/http closes the connection after reply when a 100-continue body was not
// read to EOF, even if the drain consumed it.
type continueReader struct {
	io.ReadCloser
	c          *conn
	sawEOF     bool
	closedFlag bool
}

func (r *continueReader) Read(p []byte) (int, error) {
	if !r.closedFlag {
		r.c.maybeWriteContinue()
	}
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		r.sawEOF = true
	}
	return n, err
}

func (r *continueReader) Close() error {
	r.closedFlag = true
	return r.ReadCloser.Close()
}
