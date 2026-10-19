package web

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func responseKeyFixture() (*requestInfo, *http.Request, *responseRound) {
	info := &requestInfo{host: "campfire.test", origin: "http://campfire.test"}
	r := httptest.NewRequest("GET", "/searches?q=parrot", nil)
	r.Form = map[string][]string{"q": {"parrot"}}
	return info, r, &responseRound{version: 42, user: 7, gzip: true}
}

// TestResponseCacheKeyEnvelope pins the zero-allocation envelope of the
// whole-response key: the preflight length equals the built key's length, a
// shape inside the envelope builds and hashes without allocating, and a shape
// outside it (a large cookie, a long query, or more form keys than the stack
// array holds) is rejected by the preflight before the builder can grow a
// slice. Without the preflight each of these would allocate.
func TestResponseCacheKeyEnvelope(t *testing.T) {
	info, r, round := responseKeyFixture()
	size, ok := responseKeySize(info, r, round, 42)
	if !ok {
		t.Fatal("ordinary key reported unbounded")
	}
	var buf [responseKeyBufSize]byte
	if got := len(appendResponseCacheKey(buf[:0], info, r, round, 42)); got != size {
		t.Fatalf("preflight size %d != built key %d", size, got)
	}
	// The identity coding is a different length; the mirror must track it.
	identityRound := *round
	identityRound.gzip = false
	isize, ok := responseKeySize(info, r, &identityRound, 42)
	if !ok {
		t.Fatal("identity key reported unbounded")
	}
	if got := len(appendResponseCacheKey(buf[:0], info, r, &identityRound, 42)); got != isize {
		t.Fatalf("identity preflight size %d != built key %d", isize, got)
	}
	if allocs := testing.AllocsPerRun(50, func() {
		var b [responseKeyBufSize]byte
		_ = sha256.Sum256(appendResponseCacheKey(b[:0], info, r, round, 42))
	}); allocs != 0 {
		t.Fatalf("bounded key build allocated %v times", allocs)
	}

	t.Run("large cookie", func(t *testing.T) {
		info, r, round := responseKeyFixture()
		r.Header.Set("Cookie", strings.Repeat("c", 4096))
		if _, ok := responseKeySize(info, r, round, 42); ok {
			t.Fatal("large cookie reported bounded")
		}
		if allocs := testing.AllocsPerRun(50, func() {
			if _, ok := responseKeySize(info, r, round, 42); ok {
				var b [responseKeyBufSize]byte
				_ = sha256.Sum256(appendResponseCacheKey(b[:0], info, r, round, 42))
			}
		}); allocs != 0 {
			t.Fatalf("preflight allocated %v times", allocs)
		}
	})

	t.Run("long query", func(t *testing.T) {
		info, r, round := responseKeyFixture()
		r.RequestURI = "/searches?q=" + strings.Repeat("x", 4096)
		if _, ok := responseKeySize(info, r, round, 42); ok {
			t.Fatal("long query reported bounded")
		}
	})

	t.Run("form key cap", func(t *testing.T) {
		info, r, round := responseKeyFixture()
		form := map[string][]string{}
		for i := 0; i < maxResponseFormKeys; i++ {
			form[fmt.Sprintf("k%02d", i)] = []string{"v"}
		}
		r.Form = form
		size, ok := responseKeySize(info, r, round, 42)
		if !ok {
			t.Fatalf("%d form keys reported unbounded", maxResponseFormKeys)
		}
		var b [responseKeyBufSize]byte
		if got := len(appendResponseCacheKey(b[:0], info, r, round, 42)); got != size {
			t.Fatalf("preflight size %d != built key %d", size, got)
		}
		if allocs := testing.AllocsPerRun(50, func() {
			var b [responseKeyBufSize]byte
			_ = sha256.Sum256(appendResponseCacheKey(b[:0], info, r, round, 42))
		}); allocs != 0 {
			t.Fatalf("capped form key build allocated %v times", allocs)
		}
		form[fmt.Sprintf("k%02d", maxResponseFormKeys)] = []string{"v"}
		r.Form = form
		if _, ok := responseKeySize(info, r, round, 42); ok {
			t.Fatalf("%d form keys reported bounded", maxResponseFormKeys+1)
		}
	})
}

// TestSidebarPageKeyEnvelope pins the same envelope for the sidebar page key:
// a long user name is rejected by the preflight rather than grown through
// append.
func TestSidebarPageKeyEnvelope(t *testing.T) {
	short := database.User{ID: 3, Name: "fixture", Role: 1}
	size, ok := sidebarPageKeySize(7, 1, short, false)
	if !ok {
		t.Fatal("ordinary sidebar key reported unbounded")
	}
	var buf [maxSidebarPageKeySize]byte
	if got := len(appendSidebarPageKey(buf[:0], 7, 1, short, false)); got != size {
		t.Fatalf("preflight size %d != built key %d", size, got)
	}
	if allocs := testing.AllocsPerRun(50, func() {
		var b [maxSidebarPageKeySize]byte
		_ = sha256.Sum256(appendSidebarPageKey(b[:0], 7, 1, short, false))
	}); allocs != 0 {
		t.Fatalf("bounded sidebar key build allocated %v times", allocs)
	}

	long := database.User{ID: 3, Name: strings.Repeat("n", 4096), Role: 1}
	if _, ok := sidebarPageKeySize(7, 1, long, false); ok {
		t.Fatal("long name reported bounded")
	}
	if allocs := testing.AllocsPerRun(50, func() {
		if _, ok := sidebarPageKeySize(7, 1, long, false); ok {
			var b [maxSidebarPageKeySize]byte
			_ = sha256.Sum256(appendSidebarPageKey(b[:0], 7, 1, long, false))
		}
	}); allocs != 0 {
		t.Fatalf("sidebar preflight allocated %v times", allocs)
	}
}
