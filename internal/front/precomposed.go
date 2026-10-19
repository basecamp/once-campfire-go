package front

// The public chain's precomposed seam (the web layer's ENGINE-49 head lane).
//
// The application builds a read page's response head as one sorted byte block
// and hands it to the owned loop in a single call (WritePrecomposed), skipping
// the http.Header map entirely. The front's capture wrapper sits between the
// application and the public response policy on every public request, and it
// used to be deliberately opaque (no Unwrap), which stopped the web layer's
// receiver walk: `framed` was false and every public read response paid the
// map path (13 Set calls, the header sort, the ETag/Vary/Content-Length
// formatting) that the lane exists to remove.
//
// The wrapper is transparent when — and only when — the front can complete a
// precomposed response, and it stays in the path by being the receiver itself:
// the application's WritePrecomposed call lands on the front wrapper, which
// splices in the header lines the live map carries but the block does not (the
// front's X-Cache marker above all, which the map path emits) and forwards to
// the next receiver.
//
// "Can complete" has two parts, and they are answered where each can be
// answered honestly:
//
//   - the chain must end in a writer that actually emits a precomposed
//     response (the owned loop). A net/http target listener — the TLS/ACME
//     public listener, the httptest harness — has none: the front's own
//     policy wrapper accepts a precomposed call but forwards it, and a
//     forwarder with nothing behind it would swallow the response. That is a
//     question about the chain, so it is answered here, at the writer the
//     application sees, and when the answer is no the application is told no:
//     the capture is opaque, or the policy wrapper's own blocking surface is
//     installed (bypassWriter). Nothing silently succeeds without an emitter;
//   - no response policy in the chain may transform the response. That is a
//     question about the *response*, not the request: the public compression
//     policy knows zstd and the application's selector does not, so an
//     identity body on a zstd-selected request would be compressed by the map
//     path and would go out uncompressed if framed, while an already-encoded
//     body — zstd included — is a pass-through. The web layer therefore asks
//     the policy at its framing decision, with the response's actual wire
//     encoding (precomposedGate, internal/web/framing.go;
//     publicResponse.framePrecomposed). A framed response is always one the
//     policy would not have transformed.
//
// The wrappers live inside objects the request already owns — the capture on
// the miss path, the policy wrapper on the bypass path — so installing a seam
// adds no allocation there.
//
// The front cache can never store a response the lane emits: the application's
// recorded pages carry "Cache-Control: max-age=0, private, must-revalidate",
// and lifetime() requires a `public` directive, so the capture is a
// pass-through (the head block, not the map, is the emitted head either way).

import (
	"bytes"
	"net/http"
)

// precomposedReceiver mirrors internal/web's precomposed seam. The front
// cannot import internal/web (web imports front), so the interface is
// restated; Go's structural typing makes the two identical.
type precomposedReceiver interface {
	WritePrecomposed(status int, head []byte, body [][]byte) error
}

// precomposedForwarder is implemented by the front's own policy wrappers: they
// accept a precomposed response but forward it rather than emit it, so the
// gate must look past them for the writer that actually emits.
type precomposedForwarder interface {
	forwardsPrecomposed()
}

// emitterIn reports whether the chain ends in a writer that emits a
// precomposed response: the owned loop's response. The walk follows Unwrap to
// the end, skipping the front's own forwarding wrappers.
func emitterIn(w http.ResponseWriter) bool {
	for {
		if _, ok := w.(precomposedReceiver); ok {
			if _, forwards := w.(precomposedForwarder); !forwards {
				return true
			}
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = wrapper.Unwrap()
	}
}

// frameEligible reports whether the front may let the application frame at all
// on this chain: an emitter must exist downstream. Whether *this response* may
// be framed is the policy's answer, asked at the framing decision where the
// response's encoding is known (publicResponse.framePrecomposed).
func frameEligible(w http.ResponseWriter) bool {
	return emitterIn(w)
}

// findPrecomposedReceiver walks the writer chain (the same Unwrap walk the web
// layer's lookup uses) for a precomposed receiver.
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

// captureWriterFor returns the writer the application sees on the miss path:
// the capture itself when the front cannot complete a precomposed response
// (the application then takes the byte-identical map path), or the capture's
// own embedded seam when it can. The seam is a field of the capture, so the
// transparent writer adds no allocation to the request.
func captureWriterFor(capture *recordResponse) http.ResponseWriter {
	if !frameEligible(capture.ResponseWriter) {
		return capture
	}
	capture.seam = captureWriter{capture}
	return &capture.seam
}

// bypassWriter returns the writer the application sees on the bypass path (the
// front cache disabled, or a request it will not serve). The policy wrapper
// owns both surfaces — the seam and the blocking counterpart — so neither
// costs an allocation. A chain without a policy wrapper (compression disabled)
// has no request-owned home: the seam is the front's only marker home there, so
// an emitter-backed bypass installs one wrapper per request, and a chain with
// no emitter is passed through unchanged (it advertises nothing).
func bypassWriter(w http.ResponseWriter) http.ResponseWriter {
	policy, ok := w.(*publicResponse)
	if ok {
		if frameEligible(w) {
			policy.seam = seamWriter{policy}
			return &policy.seam
		}
		policy.block = sealedWriter{policy}
		return &policy.block
	}
	if frameEligible(w) {
		// Compression disabled: one wrapper per bypassed request, the
		// documented cost of the marker home (see the package comment).
		return &seamWriter{w}
	}
	return w
}

// captureWriter is the capture wrapper the public miss path installs when the
// front can complete a precomposed response: the ordinary recordResponse
// bookkeeping plus the precomposed seam. It is the only front wrapper that
// unwraps, so the web layer's receiver walk reaches the loop through it while
// the application's precomposed write still lands on the front.
type captureWriter struct {
	*recordResponse
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *captureWriter) forwardsPrecomposed()        {}

// WritePrecomposed emits the application's precomposed response through the
// front. The wrapper takes the status for its bookkeeping (so the deferred
// write paths cannot emit a second head) and forwards through the seam.
//
// Nothing is captured: the application's precomposed responses are its own
// cached pages, which are private and so never accepted by lifetime().
func (w *captureWriter) WritePrecomposed(status int, head []byte, body [][]byte) error {
	w.status = status
	return forwardPrecomposed(w.ResponseWriter, status, head, body)
}

// seamWriter is the bypass path's seam: it carries no capture (the cache round
// did not run), only the precomposed forwarding, which keeps the front's
// X-Cache marker on the wire when the application frames the response.
type seamWriter struct {
	http.ResponseWriter
}

func (w *seamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *seamWriter) forwardsPrecomposed()        {}
func (w *seamWriter) WritePrecomposed(status int, head []byte, body [][]byte) error {
	return forwardPrecomposed(w.ResponseWriter, status, head, body)
}

// sealedWriter hides a downstream writer's precomposed seam while forwarding
// the plain ResponseWriter surface: it has no Unwrap and no WritePrecomposed,
// so the web layer's receiver walk stops at it and the application takes the
// byte-identical map path. It is what the bypass path installs when the front
// cannot complete a precomposed response but a policy wrapper would otherwise
// advertise one.
type sealedWriter struct {
	http.ResponseWriter
}

// forwardPrecomposed hands the application's head block and body to the next
// precomposed receiver in the chain, with the header lines the live map
// carries but the block does not spliced in. The receiver's own WriteHeader
// policy never ran on this path, so a policy that must reach the emitted head
// (the compression wrapper's Vary) implements WritePrecomposed itself and
// forwards in turn.
func forwardPrecomposed(next http.ResponseWriter, status int, head []byte, body [][]byte) error {
	receiver := findPrecomposedReceiver(next)
	if receiver == nil {
		// Unreachable: a seam is installed only after the gate found an
		// emitter, and nothing rewrites the chain in between. There is no map
		// to fall back to (the framed path never built one).
		return nil
	}
	return receiver.WritePrecomposed(status, spliceMissingHeaders(head, next.Header(), status), body)
}

// headerLine is one rendered header line waiting to be spliced.
type headerLine struct{ name, value string }

// spliceMissingHeaders completes the application's head block with the header
// lines the map path would emit on top of it.
//
// The application's 200 block is rendered from the same values the map path
// emits — the header block "in the map path's exact order", as its own
// documentation puts it — so the front's X-Cache marker, which the block never
// carries, is the whole job; the map walk that guarantees the rest is skipped
// on that path because it costs ~370 ns, a fifth of the lane's saving. Any
// other status is a suppression of the 200 block (the 304 drops the entity
// headers, Content-Encoding among them), so those blocks are completed from
// the map, which is the shape the map path's own 304 has.
//
// The missing lines are decided against the block as it arrived — the checks
// are independent of the insert order — and the block is grown once for the
// whole run, with room for the policy's own line, so a tight block costs one
// replacement rather than one per line. A block with spare capacity (the
// application carves it from the request arena) allocates nothing.
func spliceMissingHeaders(head []byte, h http.Header, status int) []byte {
	var buf [8]headerLine
	missing := buf[:0]
	add := func(name, value string) {
		if value == "" || headHasLine(head, name) {
			return
		}
		missing = append(missing, headerLine{name: name, value: value})
	}
	if marker := h.Get("X-Cache"); marker != "" {
		add("X-Cache", marker)
	}
	if status != http.StatusOK {
		for name, values := range h {
			for _, value := range values {
				add(name, value)
			}
		}
	}
	if len(missing) == 0 {
		return head
	}
	need := policyLineRoom
	for _, line := range missing {
		need += len(line.name) + len(line.value) + 4
	}
	if cap(head)-len(head) < need {
		grown := make([]byte, len(head), len(head)+need)
		copy(grown, head)
		head = grown
	}
	for _, line := range missing {
		head = insertHeadLine(head, line.name, line.value)
	}
	return head
}

// policyLineRoom is the room a growth reserves for the lines the response
// policy adds after the splice (the compression wrapper's Vary). One
// replacement block then serves the whole run.
const policyLineRoom = 40

// headHasLine reports whether a head block already carries a header line with
// this name. Presence is tested at a line start, so a value that happens to
// contain the name cannot suppress the insert.
func headHasLine(head []byte, name string) bool {
	var needleBuf [80]byte
	needle := append(needleBuf[:0], '\r', '\n')
	needle = append(needle, name...)
	needle = append(needle, ':', ' ')
	return bytes.HasPrefix(head, needle[2:]) || bytes.Contains(head, needle)
}

// insertHeadLine inserts one rendered "Name: value\r\n" line into a head block
// sorted by canonical header name, keeping that order. The line is built in a
// stack buffer, so the call allocates nothing when the block has spare
// capacity.
func insertHeadLine(head []byte, name, value string) []byte {
	if headHasLine(head, name) {
		return head
	}
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
	if cap(head)-len(head) >= len(line) {
		head = head[:len(head)+len(line)]
		copy(head[at+len(line):], head[at:])
		copy(head[at:], line)
		return head
	}
	grown := make([]byte, 0, len(head)+len(line))
	grown = append(grown, head[:at]...)
	grown = append(grown, line...)
	return append(grown, head[at:]...)
}
