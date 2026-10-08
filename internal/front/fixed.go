package front

// Fixed routes: a small set of paths whose response is process-constant —
// the health check /up — are answered by the front from a table captured the
// first time the application answers them, instead of running the application
// on every request. The capture is exact: the first unconditional GET runs the
// full chain (compression, cache bookkeeping, sessions, the web server), the
// response is recorded whole (headers as cloned under the compression layer,
// body after negotiation), and the same chain is run once more with the other
// Accept-Encoding so both wire representations (identity and gzip) are
// stored. Every later request replays the captured bytes with the same replay
// the ordinary cache uses, byte for byte: validators, Vary, Content-Encoding,
// the captured X-Cache value and the chunked framing the application path
// also produced. The application only runs again if the replays cannot serve
// the request (a conditional, a non-negotiable encoding, or a capture failure
// such as a non-200 or a Set-Cookie).

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
)

// fixedRoutes lists the paths whose entire response is captured once and
// replayed. Only exact GET paths with no conditional headers participate.
var fixedRoutes = map[string]bool{"/up": true}

// fixedLimit bounds one captured fixed body; the health body is ~100 bytes.
const fixedLimit = 8 << 10

// fixedCandidate reports whether r may fill or replay a fixed route.
func fixedCandidate(r *http.Request) bool {
	return r.Method == "GET" && fixedRoutes[r.URL.Path] && r.Header.Get("If-None-Match") == "" && r.Header.Get("If-Modified-Since") == ""
}

// fixedCap bounds the number of stored pairs. The response of a fixed route
// can vary with the Accept header (the application negotiates format and
// adds Vary: Accept), so each distinct Accept value gets its own pair; the
// value is attacker-chosen, so the table is capped and further captures are
// abandoned (existing pairs keep replaying, new Accept values go to the
// application).
const fixedCap = 16

// fixedPair holds the two wire representations of one fixed route.
type fixedPair struct {
	identity, gzip *cacheEntry
}

// forRequest picks the stored representation by the same negotiation the
// application path used to produce the capture (front.Deflate's
// httpcompat.Encoding). An encoding the pair cannot serve — the client
// rejects both gzip and identity — falls through to the application, which
// owns the 406 policy.
func (p *fixedPair) forRequest(r *http.Request) *cacheEntry {
	switch encoding(r.Header.Get("Accept-Encoding")) {
	case "gzip":
		return p.gzip
	case "identity":
		return p.identity
	default:
		return nil
	}
}

// fixedEntry converts one captured pass into a fixed entry, or nil when the
// response cannot be replayed safely (non-200, overflow, empty body, any
// Set-Cookie — a captured cookie would go stale and leak into later replays).
func fixedEntry(capture *recordResponse) *cacheEntry {
	if capture.status != 200 || capture.overflow || len(capture.body.Bytes()) == 0 {
		return nil
	}
	for _, value := range capture.header.Values("Set-Cookie") {
		if value != "" {
			return nil
		}
	}
	body := bytes.Clone(capture.body.Bytes())
	entry := &cacheEntry{header: capture.header, body: body, status: capture.status, size: int64(len(body) + 512)}
	// The fixed replay keeps its captured X-Cache value (the fill's miss
	// marker), so no substitution is applied.
	entry.recorded = newRecordedResponse(capture.wire(), capture.status, body, "")
	return entry
}

// captureSink absorbs a synthesized capture run: its own header map and body,
// nothing forwarded anywhere.
type captureSink struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (s *captureSink) Header() http.Header         { return s.header }
func (s *captureSink) WriteHeader(status int)      { s.status = status }
func (s *captureSink) Write(p []byte) (int, error) { return s.body.Write(p) }
func (s *captureSink) Flush()                      {}

// fixedPairKey scopes a fixed route to one host and the exact Accept header
// value it was captured under: the application negotiates format from Accept
// and emits Vary: Accept for accept-driven responses, so a replay is only
// safe for the Accept value that produced it.
func fixedPairKey(r *http.Request) string {
	return r.Host + "\x00" + r.URL.Path + "\x00" + r.Header.Get("Accept")
}

// runFixed serves the request through the chain once more under the other
// content encoding, capturing the response whole; nil when the pass cannot
// fill the pair.
func runFixed(next http.Handler, r *http.Request, acceptEncoding string) *cacheEntry {
	clone := r.Clone(r.Context())
	clone.Header.Set("Accept-Encoding", acceptEncoding)
	sink := &captureSink{header: http.Header{}}
	capture := &recordResponse{ResponseWriter: sink, max: fixedLimit, captureAlways: true}
	capture.Header().Set("X-Cache", "miss")
	next.ServeHTTP(capture, clone)
	if capture.status == 0 {
		capture.WriteHeader(200)
	}
	return fixedEntry(capture)
}

// pairFor is the fixed-pair lookup used on every request.
func (c *Cache) pairFor(r *http.Request) *fixedPair {
	if !c.FixedRoutes || !fixedCandidate(r) {
		return nil
	}
	c.mu.Lock()
	pair := c.fixed[fixedPairKey(r)]
	c.mu.Unlock()
	return pair
}

// fillFixed runs after a fixed candidate has been served by the application:
// the pass just taken is one representation, and the chain runs once more
// under the other Accept-Encoding to complete the pair. The pair is stored
// only when both captures are clean and decode to the same content; a failed
// fill marks the route failed so the application path keeps serving without
// retrying the double pass on every request.
func (c *Cache) fillFixed(r *http.Request, next http.Handler, capture *recordResponse) {
	first := fixedEntry(capture)
	if first == nil {
		c.fixedFailed = true
		return
	}
	c.mu.Lock()
	over := len(c.fixed) >= fixedCap
	c.mu.Unlock()
	if over {
		// The table is full of distinct Accept values; stop expanding it and
		// serve further Accept values from the application.
		c.fixedFailed = true
		return
	}
	var identity, gzipVariant *cacheEntry
	if capture.header.Get("Content-Encoding") == "gzip" {
		gzipVariant = first
		identity = runFixed(next, r, "identity")
	} else {
		identity = first
		gzipVariant = runFixed(next, r, "gzip")
	}
	if identity == nil || gzipVariant == nil {
		c.fixedFailed = true
		return
	}
	if !fixedBodiesAgree(identity, gzipVariant) {
		// The synthetic pass produced different content; keep the
		// application path rather than replaying a lie.
		c.fixedFailed = true
		return
	}
	c.mu.Lock()
	c.fixed[fixedPairKey(r)] = &fixedPair{identity: identity, gzip: gzipVariant}
	c.mu.Unlock()
}

// fixedBodiesAgree verifies the gzip representation decodes to exactly the
// identity bytes: the two captures are one response under two encodings.
func fixedBodiesAgree(identity, gzipVariant *cacheEntry) bool {
	reader, err := gzip.NewReader(bytes.NewReader(gzipVariant.body))
	if err != nil {
		return false
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return false
	}
	return bytes.Equal(decoded, identity.body)
}
