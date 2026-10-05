package piececache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"io"
	"os"
	"sync"
	"testing"
)

// mustMember returns a complete gzip member of raw, the form Put stores.
// BestSpeed and OS=3 mirror what front.Deflate writes.
func mustMember(tb testing.TB, raw []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		tb.Fatalf("gzip.NewWriterLevel: %v", err)
	}
	zw.Header.OS = 3
	if _, err := zw.Write(raw); err != nil {
		tb.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		tb.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func memberSize(key string, raw, member []byte) int {
	return len(key) + len(raw) + len(member) + entryOverhead
}

// TestPolicyParityWithFragmentCache pins the numbers shared with
// internal/web/fragments.go: the 240-byte per-entry charge (and, exercised by
// TestPutRejectsOversizedEntry and TestLRUEvictsOldestAndPrunesTo75Percent,
// the limit/4 oversize divisor and the prune-to-75% target).
func TestPolicyParityWithFragmentCache(t *testing.T) {
	if entryOverhead != 240 {
		t.Fatalf("entryOverhead = %d, want 240 as in internal/web/fragments.go", entryOverhead)
	}
}

func decodeMember(member []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(member))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// TestPutDefensiveCopy is the poisoning test: the cache must hold its own
// copies, so writes to the caller's buffers after Put can never show up in a
// cached piece.
func TestPutDefensiveCopy(t *testing.T) {
	cache := New(1 << 20)
	raw := []byte("piece raw bytes, enough to be worth copying")
	member := mustMember(t, raw)
	rawCopy := append([]byte(nil), raw...)
	memberCopy := append([]byte(nil), member...)

	if entry, ok := cache.Put("room/1/v3", raw, member); !ok || entry == nil {
		t.Fatalf("Put rejected a small entry: entry=%v ok=%v", entry, ok)
	}

	// Poison the caller's buffers after Put, then read the cache back.
	for i := range raw {
		raw[i] = 0xEE
	}
	for i := range member {
		member[i] = 0x55
	}

	entry := cache.Get("room/1/v3")
	if entry == nil {
		t.Fatal("Get after Put missed")
	}
	if !bytes.Equal(entry.Raw, rawCopy) {
		t.Fatalf("Raw = %q, want %q", entry.Raw, rawCopy)
	}
	if !bytes.Equal(entry.Member, memberCopy) {
		t.Fatalf("Member mutated with the caller buffer: got %d bytes, want %d", len(entry.Member), len(memberCopy))
	}
	if want := sha256.Sum256(rawCopy); entry.Digest != want {
		t.Fatalf("Digest = %x, want %x", entry.Digest, want)
	}
	// The poisoned caller buffers are not the cached entry's storage.
	if len(entry.Member) > 0 && &entry.Member[0] == &member[0] {
		t.Fatal("Entry.Member aliases the caller's buffer")
	}
	if len(entry.Raw) > 0 && &entry.Raw[0] == &raw[0] {
		t.Fatal("Entry.Raw aliases the caller's buffer")
	}
}

func TestGetMiss(t *testing.T) {
	cache := New(1 << 20)
	if entry := cache.Get("nothing/here"); entry != nil {
		t.Fatalf("Get on empty cache = %v, want nil", entry)
	}
}

// TestGetReturnsSameImmutableView pins the no-copy contract: repeated Gets
// return the same pointer, and a replacement publishes a new entry without
// invalidating readers of the old one.
func TestGetReturnsSameImmutableView(t *testing.T) {
	cache := New(1 << 20)
	raw := []byte("first")
	if _, ok := cache.Put("k", raw, mustMember(t, raw)); !ok {
		t.Fatal("Put rejected")
	}
	first := cache.Get("k")
	second := cache.Get("k")
	if first == nil || second == nil {
		t.Fatal("Get missed after Put")
	}
	if first != second {
		t.Fatal("Get copied or rebuilt the entry; contract is a no-copy immutable view")
	}

	raw2 := []byte("second")
	replacement, ok := cache.Put("k", raw2, mustMember(t, raw2))
	if !ok {
		t.Fatal("replacement Put rejected")
	}
	if got := cache.Get("k"); got != replacement {
		t.Fatal("replacement entry is not visible after Put")
	}
	if !bytes.Equal(first.Raw, raw) {
		t.Fatalf("old view mutated by replacement: %q", first.Raw)
	}
	if !bytes.Equal(replacement.Raw, raw2) {
		t.Fatalf("replacement Raw = %q, want %q", replacement.Raw, raw2)
	}
}

func TestEntryDigestIsSHA256OfRaw(t *testing.T) {
	entry := NewEntry([]byte("digest me"), []byte("not checked"))
	if want := sha256.Sum256([]byte("digest me")); entry.Digest != want {
		t.Fatalf("Digest = %x, want %x", entry.Digest, want)
	}
}

func TestNewEntryDefensiveCopy(t *testing.T) {
	raw := []byte("raw bytes")
	member := []byte("member bytes")
	entry := NewEntry(raw, member)
	for i := range raw {
		raw[i] = 'x'
	}
	for i := range member {
		member[i] = 'y'
	}
	if !bytes.Equal(entry.Raw, []byte("raw bytes")) {
		t.Fatalf("Raw aliased the caller: %q", entry.Raw)
	}
	if !bytes.Equal(entry.Member, []byte("member bytes")) {
		t.Fatalf("Member aliased the caller: %q", entry.Member)
	}
}

func TestPutRejectsOversizedEntry(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), 300)
	member := mustMember(t, raw)
	size := memberSize("key", raw, member)

	// limit/4 is one byte under the entry's charged size: reject.
	cache := New(size*4 - 1)
	if entry, ok := cache.Put("key", raw, member); ok || entry != nil {
		t.Fatalf("Put accepted size %d over limit/4 of %d", size, size*4-1)
	}
	if got := cache.Get("key"); got != nil {
		t.Fatal("rejected entry was stored")
	}
	if cache.bytes != 0 || len(cache.entries) != 0 {
		t.Fatalf("rejected entry charged %d bytes, %d entries", cache.bytes, len(cache.entries))
	}

	// Exactly limit/4 is allowed: the check is strictly greater-than.
	cache = New(size * 4)
	if entry, ok := cache.Put("key", raw, member); !ok || entry == nil {
		t.Fatal("Put rejected an entry of exactly limit/4")
	}
	if cache.bytes != size {
		t.Fatalf("charged %d bytes, want %d", cache.bytes, size)
	}
}

func TestLRUEvictsOldestAndPrunesTo75Percent(t *testing.T) {
	makePiece := func(name string) (string, []byte, []byte, int) {
		raw := bytes.Repeat([]byte(name), 200)
		member := mustMember(t, raw)
		return name, raw, member, memberSize(name, raw, member)
	}
	keyA, rawA, memberA, sizeA := makePiece("a")
	keyB, rawB, memberB, sizeB := makePiece("b")
	keyC, rawC, memberC, sizeC := makePiece("c")
	keyD, rawD, memberD, sizeD := makePiece("d")
	keyE, rawE, memberE, sizeE := makePiece("e")
	if sizeA != sizeB || sizeA != sizeC || sizeA != sizeD || sizeA != sizeE {
		t.Fatalf("fixture error: sizes differ: %d %d %d %d %d", sizeA, sizeB, sizeC, sizeD, sizeE)
	}
	size := sizeA

	// Budget 4s accepts each entry (s <= limit/4) and fits a+b+c+d exactly.
	// Inserting e overflows and must prune to 75% (removing b and c), not
	// merely back under 100%.
	cache := New(4 * size)
	for _, piece := range []struct {
		key         string
		raw, member []byte
	}{
		{keyA, rawA, memberA},
		{keyB, rawB, memberB},
		{keyC, rawC, memberC},
		{keyD, rawD, memberD},
	} {
		if _, ok := cache.Put(piece.key, piece.raw, piece.member); !ok {
			t.Fatalf("Put(%q) rejected", piece.key)
		}
	}
	if cache.bytes != 4*size {
		t.Fatalf("charged %d, want %d", cache.bytes, 4*size)
	}

	// Touch a: the eviction order is now a (front), d, c, b (back).
	if cache.Get(keyA) == nil {
		t.Fatal("Get(a) missed")
	}
	if _, ok := cache.Put(keyE, rawE, memberE); !ok {
		t.Fatal("Put(e) rejected")
	}

	if cache.Get(keyB) != nil {
		t.Fatal("b survived; it was the least recently used")
	}
	if cache.Get(keyC) != nil {
		t.Fatal("c survived; pruning must go to 75%, not just under 100%")
	}
	if cache.Get(keyA) == nil {
		t.Fatal("a was evicted after being touched")
	}
	if cache.Get(keyD) == nil {
		t.Fatal("d was evicted before the untouched c")
	}
	if cache.Get(keyE) == nil {
		t.Fatal("newest entry was evicted by its own insertion")
	}
	if cache.bytes != 3*size {
		t.Fatalf("charged %d after eviction, want %d", cache.bytes, 3*size)
	}
}

func TestPutReplacesSameKeyAccounting(t *testing.T) {
	cache := New(1 << 20)
	raw1 := bytes.Repeat([]byte("1"), 500)
	member1 := mustMember(t, raw1)
	first, ok := cache.Put("v1", raw1, member1)
	if !ok {
		t.Fatal("first Put rejected")
	}
	size1 := memberSize("v1", raw1, member1)

	raw2 := bytes.Repeat([]byte("2"), 300)
	member2 := mustMember(t, raw2)
	second, ok := cache.Put("v1", raw2, member2)
	if !ok {
		t.Fatal("replacement Put rejected")
	}
	size2 := memberSize("v1", raw2, member2)
	if size1 == size2 {
		t.Fatal("fixture error: sizes must differ to prove re-accounting")
	}
	if cache.bytes != size2 {
		t.Fatalf("charged %d after replace, want %d (old %d double-counted?)", cache.bytes, size2, size1)
	}
	if len(cache.entries) != 1 {
		t.Fatalf("%d entries after replace, want 1", len(cache.entries))
	}
	if got := cache.Get("v1"); got != second {
		t.Fatal("Get returned the old entry after replace")
	}
	if !bytes.Equal(first.Raw, raw1) {
		t.Fatalf("old view mutated: %q", first.Raw)
	}
}

// TestPutRejectedReplacementKeepsOld: a replacement that cannot be cached is
// all-or-nothing; the previous entry stays.
func TestPutRejectedReplacementKeepsOld(t *testing.T) {
	cache := New(1 << 20) // limit/4 = 256 KiB
	raw := []byte("small")
	if _, ok := cache.Put("k", raw, mustMember(t, raw)); !ok {
		t.Fatal("Put rejected small entry")
	}
	big := bytes.Repeat([]byte("z"), 300<<10)
	if entry, ok := cache.Put("k", big, mustMember(t, big)); ok || entry != nil {
		t.Fatalf("oversized replacement accepted: %v", entry)
	}
	got := cache.Get("k")
	if got == nil || !bytes.Equal(got.Raw, raw) {
		t.Fatalf("old entry lost after rejected replacement: %v", got)
	}
}

func TestZeroLimitDisablesStorage(t *testing.T) {
	cache := New(0)
	raw := []byte("data")
	if entry, ok := cache.Put("k", raw, mustMember(t, raw)); ok || entry != nil {
		t.Fatalf("Put on disabled cache = %v, %v; want nil, false", entry, ok)
	}
	if got := cache.Get("k"); got != nil {
		t.Fatalf("disabled cache returned %v", got)
	}
}

// TestConcurrentReplaceIsAtomic asks the race detector and a consistency
// assertion the only question that matters: can a reader ever observe a mix of
// two entries? Each entry carries raw, member and digest that must agree.
func TestConcurrentReplaceIsAtomic(t *testing.T) {
	cache := New(1 << 20)
	const key = "room/1/messages/v42"
	payloads := make([][]byte, 4)
	members := make([][]byte, len(payloads))
	sizes := make([]int, len(payloads))
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 1024)
		members[i] = mustMember(t, payloads[i])
		sizes[i] = memberSize(key, payloads[i], members[i])
		if _, ok := cache.Put(key, payloads[i], members[i]); !ok {
			t.Fatal("setup Put rejected")
		}
	}

	const iterations = 200
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				index := (w + i) % len(payloads)
				if entry, ok := cache.Put(key, payloads[index], members[index]); !ok || entry == nil {
					t.Errorf("Put rejected in goroutine %d", w)
					return
				}
			}
		}(w)
	}
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				entry := cache.Get(key)
				if entry == nil {
					t.Error("Get missed during concurrent replace")
					return
				}
				if sha256.Sum256(entry.Raw) != entry.Digest {
					t.Error("digest does not match Raw")
					return
				}
				decoded, err := decodeMember(entry.Member)
				if err != nil {
					t.Errorf("member does not decode: %v", err)
					return
				}
				if !bytes.Equal(decoded, entry.Raw) {
					t.Error("member does not decode to Raw: torn entry")
					return
				}
			}
		}()
	}
	wg.Wait()

	cache.mu.Lock()
	count := len(cache.entries)
	bytes := cache.bytes
	cache.mu.Unlock()
	if count != 1 {
		t.Fatalf("%d entries after concurrent replace, want 1", count)
	}
	matched := false
	for _, size := range sizes {
		matched = matched || bytes == size
	}
	if !matched {
		t.Fatalf("charged %d bytes after concurrent replace, want one of %v", bytes, sizes)
	}
}

func TestLimitFromEnv(t *testing.T) {
	t.Setenv("CAMPFIRE_FRAGMENT_CACHE_MB", "8")
	got, err := LimitFromEnv()
	if err != nil || got != 8<<20 {
		t.Fatalf("LimitFromEnv() = %d, %v; want %d, nil", got, err, 8<<20)
	}
	t.Setenv("CAMPFIRE_FRAGMENT_CACHE_MB", "0")
	if got, err := LimitFromEnv(); err != nil || got != 0 {
		t.Fatalf("LimitFromEnv() = %d, %v; want 0, nil", got, err)
	}
	t.Setenv("CAMPFIRE_FRAGMENT_CACHE_MB", "1048576")
	if got, err := LimitFromEnv(); err != nil || got != 1<<20<<20 {
		t.Fatalf("LimitFromEnv() at max = %d, %v", got, err)
	}
	for _, bad := range []string{"nope", "-1", "1048577", "3.5"} {
		t.Setenv("CAMPFIRE_FRAGMENT_CACHE_MB", bad)
		if _, err := LimitFromEnv(); err == nil {
			t.Fatalf("LimitFromEnv() accepted %q", bad)
		}
	}
}

func TestLimitFromEnvDefault(t *testing.T) {
	previous, had := os.LookupEnv("CAMPFIRE_FRAGMENT_CACHE_MB")
	if err := os.Unsetenv("CAMPFIRE_FRAGMENT_CACHE_MB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("CAMPFIRE_FRAGMENT_CACHE_MB", previous)
		} else {
			_ = os.Unsetenv("CAMPFIRE_FRAGMENT_CACHE_MB")
		}
	})
	got, err := LimitFromEnv()
	if err != nil || got != DefaultLimit {
		t.Fatalf("LimitFromEnv() = %d, %v; want %d, nil", got, err, DefaultLimit)
	}
	if DefaultLimit != 32<<20 {
		t.Fatalf("DefaultLimit = %d, want 32 MiB", DefaultLimit)
	}
}
