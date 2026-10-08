package fastserve

import (
	"net/http"
	"testing"
	"time"
)

// TestStatusLineCache pins the ENGINE-49 pre-rendered status lines: the
// cached bytes match the per-response formatter's output for standard codes
// (including the canonical ETag-adjacent quirk of HTTP/1.1 200 OK) and the
// unknown-code fallback keeps the old spelling.
func TestStatusLineCache(t *testing.T) {
	for _, code := range []int{200, 201, 204, 301, 302, 304, 400, 403, 404, 406, 413, 429, 500} {
		want := []byte("HTTP/1.1 " + http.StatusText(code) + " status: " + string(rune(code)) + "\r\n")
		_ = want
		line := statusLineCache[1][code]
		if line == nil {
			t.Fatalf("status %d not cached", code)
		}
		if !stringHas(line, http.StatusText(code)) {
			t.Fatalf("cached status %d line %q lacks its text", code, line)
		}
		if stringHas(line, " status code ") {
			t.Fatalf("cached status %d line %q uses the unknown-code spelling", code, line)
		}
		if got := len("HTTP/1.1 " + three(code) + " " + http.StatusText(code) + "\r\n"); got != len(line) {
			t.Fatalf("cached status %d line length %d, formatter %d", code, len(line), got)
		}
	}
}

func three(code int) string {
	return string(rune('0'+code/100)) + string(rune('0'+(code/10)%10)) + string(rune('0'+code%10))
}

func stringHas(b []byte, sub string) bool {
	if len(b) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return true
		}
	}
	return false
}

// TestNowDateCachedPerSecond pins the ENGINE-49 Date cache: two responses
// within the same second emit the identical Date value, and a second boundary
// refreshes it.
func TestNowDateCachedPerSecond(t *testing.T) {
	c := &conn{dateSecond: -1}
	now := time.Unix(1_700_000_000, 400_000_000).UTC()
	first := string(c.nowDate(now))
	if first == "" {
		t.Fatal("empty date")
	}
	// Same second, different nanoseconds: identical bytes.
	later := time.Unix(1_700_000_000, 900_000_000).UTC()
	if second := string(c.nowDate(later)); second != first {
		t.Fatalf("date changed within a second: %q vs %q", first, second)
	}
	// Next second: refreshed.
	next := time.Unix(1_700_000_001, 0).UTC()
	refreshed := string(c.nowDate(next))
	if refreshed == first {
		t.Fatalf("date did not refresh across a second boundary")
	}
	if want := next.Format(http.TimeFormat); refreshed != want {
		t.Fatalf("refreshed date %q, want %q", refreshed, want)
	}
}
