package front

// The recorded-replay lane (ENGINE-62) serves a cache hit (ordinary or fixed
// route) from precomposed artifacts instead of rebuilding the response through
// the http.Header map:
//
//   - when a cacheable response is captured, the header map is snapshotted a
//     second time AFTER the public compression policy ran (the pre-policy
//     clone stays the entry's map-path header, so existing semantics are
//     untouched) and the wire head block — the map path's exact sorted,
//     sanitized lines — is rendered once with fastserve.AppendHeaderLines.
//     A 304 flavor (entity headers suppressed, exactly net/http's rule) is
//     rendered alongside when the entry carries an ETag;
//   - at replay time the writer chain is walked for a recorded receiver
//     (fastserve.response implements WriteRecorded). When one exists, the
//     status, head block and body are handed over in one call: no header map,
//     no sort, no middleware, one writev, with the receiver supplying the
//     status line, Date, the auto Content-Length/chunked framing and the
//     close decision exactly as the map path produces them.
//
// The lane only engages for HTTP/1.1 GET/HEAD requests with no request body
// against entries whose captured headers the receiver can reproduce (no Date,
// Content-Length, Transfer-Encoding, Trailer or Connection, and a
// Content-Type whenever there is a body: the receiver never sniffs). Anything
// else — including a capture whose body the compression policy encoded after
// the capture (the recorded body would be identity), conditional fallbacks,
// HTTP/1.0 and non-loop writers — takes the existing map path byte for byte.

import (
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/fastserve"
)

// recordedResponse is the immutable precomposed replay state of one cache
// entry. head and head304 are the sorted header-line blocks (no status line,
// Date or framing extras); etag is the entry's validator, used for the same
// exact If-None-Match comparison the map path applies. nil head304 cannot
// occur when etag is set (both are built together).
type recordedResponse struct {
	head    []byte
	head304 []byte
	etag    string
}

// recordedReceiver is implemented by writers that can emit a complete
// recorded response in one vectored write. fastserve.response implements it.
type recordedReceiver interface {
	WriteRecorded(status int, head []byte, body []byte) error
}

// findRecordedReceiver walks the writer chain (the same Unwrap walk the web
// layer's precomposed-receiver lookup uses) for a recorded receiver. The
// capture wrapper is deliberately opaque (see recordResponse.Unwrap's removal
// note), so a public-listener chain only reaches the owned loop and never
// skips the cache's own X-Cache bookkeeping.
func findRecordedReceiver(w http.ResponseWriter) recordedReceiver {
	for {
		if receiver, ok := w.(recordedReceiver); ok {
			return receiver
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = wrapper.Unwrap()
	}
}

// recordedRequestEligible reports whether the request supports the recorded
// lane: HTTP/1.1 (the receiver's framing mirrors the loop's HTTP/1.1 single-
// write path), GET or HEAD, and no request body (the receiver does not drain
// the way the map path's first write does).
func recordedRequestEligible(r *http.Request) bool {
	if r.ProtoMajor < 1 || (r.ProtoMajor == 1 && r.ProtoMinor < 1) {
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	return r.ContentLength == 0 && len(r.TransferEncoding) == 0
}

// ifNoneMatch reports whether any candidate in an If-None-Match value equals
// etag, the exact comparison the map path's replay applies.
func ifNoneMatch(value, etag string) bool {
	if value == "" {
		return false
	}
	for _, candidate := range strings.Split(value, ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

// recordedEligible reports whether the captured response can be replayed from
// a precomposed head block. The receiver appends Date and the framing extras
// itself, so a captured Date, Content-Length, Transfer-Encoding, Trailer or
// Connection would either duplicate or contradict them; a body with no
// captured Content-Type would be sniffed by the map path but not by the
// receiver. Every refusal falls back to the byte-identical map path.
func recordedEligible(h http.Header, status, bodyLen int) bool {
	if h == nil {
		return false
	}
	for _, name := range [...]string{"Date", "Content-Length", "Transfer-Encoding", "Trailer", "Connection"} {
		if _, ok := h[name]; ok {
			return false
		}
	}
	if bodyLen > 0 {
		if !fastserve.BodyAllowedForStatus(status) {
			return false
		}
		if _, ok := h["Content-Type"]; !ok {
			return false
		}
	}
	return true
}

// newRecordedResponse builds the replay artifacts from the post-policy header
// snapshot, or nil when the map path must serve the entry. xcache is the
// X-Cache value the replay carries: "hit" for ordinary cache hits (the
// snapshot holds the fill's "miss"), or "" to keep the captured value (fixed
// routes replay their captured miss marker, like the map path does).
func newRecordedResponse(h http.Header, status int, body []byte, xcache string) *recordedResponse {
	if !recordedEligible(h, status, len(body)) {
		return nil
	}
	snapshot := h
	if xcache != "" {
		snapshot = h.Clone()
		snapshot.Set("X-Cache", xcache)
	}
	recorded := &recordedResponse{head: fastserve.AppendHeaderLines(nil, snapshot, nil)}
	if etag := h.Get("ETag"); etag != "" {
		// net/http's suppressedHeaders(304): the entity headers are dropped
		// from the sorted block; the receiver emits no body for a 304.
		exclude := map[string]bool{"Content-Type": true, "Content-Length": true, "Transfer-Encoding": true}
		recorded.head304 = fastserve.AppendHeaderLines(nil, snapshot, exclude)
		recorded.etag = etag
	}
	return recorded
}
