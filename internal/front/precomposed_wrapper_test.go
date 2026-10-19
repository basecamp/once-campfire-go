package front

// Tests for the front's precomposed surfaces on the bypass path
// (precomposed.go): the seam the application sees when the front can complete a
// precomposed response, and the blocking surface it sees when it cannot. The
// policy wrapper advertises a seam it can only forward, so a chain with no
// emitter behind it must never be left to reach the wrapper directly.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPrecomposedBypassSurfaceHidesTheSeam pins the mechanism the bypass
// regression depends on: the blocking surface stops the receiver walk, the
// seam continues it to the emitter, and the policy wrapper on its own — which
// advertises a seam it cannot complete without an emitter behind it — is never
// what the application sees.
func TestPrecomposedBypassSurfaceHidesTheSeam(t *testing.T) {
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	emitting := &publicResponse{ResponseWriter: &emitterWriter{ResponseWriter: &shimWriter{header: http.Header{}}}, request: request, selected: "gzip"}
	if got := findPrecomposedReceiver(bypassWriter(emitting)); got == nil {
		t.Fatal("the bypass seam did not expose a receiver to the emitter")
	}
	sealed := &publicResponse{ResponseWriter: &shimWriter{header: http.Header{}}, request: request, selected: "gzip"}
	if got := findPrecomposedReceiver(bypassWriter(sealed)); got != nil {
		t.Fatalf("the blocking surface exposed %T with no emitter downstream", got)
	}
	// The policy wrapper itself does advertise the seam: that is exactly why
	// the blocking surface exists.
	if got := findPrecomposedReceiver(&publicResponse{ResponseWriter: &shimWriter{header: http.Header{}}, request: request, selected: "gzip"}); got == nil {
		t.Fatal("the policy wrapper no longer advertises the seam, so this regression cannot recur")
	}
}

// TestPrecomposedBypassWrapperAllocatesNothing pins the bypass path's wrapper
// allocation: both surfaces are fields of the request's policy wrapper, so
// installing either costs nothing. A chain without a policy wrapper
// (compression disabled) has no request-owned home; that one cold-path
// allocation is pinned too, so it cannot spread.
func TestPrecomposedBypassWrapperAllocatesNothing(t *testing.T) {
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	var got http.ResponseWriter

	emitting := &publicResponse{ResponseWriter: &emitterWriter{ResponseWriter: &shimWriter{header: http.Header{}}}, request: request, selected: "gzip"}
	if allocs := testing.AllocsPerRun(200, func() { got = bypassWriter(emitting) }); allocs != 0 {
		t.Fatalf("seam install allocated %v objects, want 0", allocs)
	}
	if got != &emitting.seam {
		t.Fatalf("seam install returned %T, want the policy wrapper's own seam", got)
	}

	sealed := &publicResponse{ResponseWriter: &shimWriter{header: http.Header{}}, request: request, selected: "gzip"}
	if allocs := testing.AllocsPerRun(200, func() { got = bypassWriter(sealed) }); allocs != 0 {
		t.Fatalf("blocking install allocated %v objects, want 0", allocs)
	}
	if got != &sealed.block {
		t.Fatalf("blocking install returned %T, want the policy wrapper's own surface", got)
	}

	bare := &emitterWriter{ResponseWriter: &shimWriter{header: http.Header{}}}
	if allocs := testing.AllocsPerRun(200, func() { got = bypassWriter(bare) }); allocs != 1 {
		t.Fatalf("no-policy seam install allocated %v objects, want the documented 1", allocs)
	}
}

// TestPrecomposedGateAnswersOnTheResponse pins the gate's criterion: the policy
// is a pass-through for any response carrying a coding of its own — a zstd body
// the application already encoded included — and for any request whose policy
// selected nothing. An identity body on a request the policy selected a coding
// for is the one shape that must take the map path, so the policy's own
// compression runs.
func TestPrecomposedGateAnswersOnTheResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		selected string
		encoding string
		want     bool
	}{
		{"identity_selected_zstd", "zstd", "", false},
		{"identity_selected_gzip", "gzip", "", false},
		{"identity_policy_selected_nothing", "", "", true},
		{"gzip_body_selected_zstd", "zstd", "gzip", true},
		{"zstd_body_selected_zstd", "zstd", "zstd", true},
		{"zstd_body_policy_selected_nothing", "", "zstd", true},
	} {
		policy := &publicResponse{selected: test.selected}
		if got := policy.FramePrecomposed(test.encoding); got != test.want {
			t.Fatalf("%s: FramePrecomposed(%q) with selected %q = %v, want %v", test.name, test.encoding, test.selected, got, test.want)
		}
	}
}
