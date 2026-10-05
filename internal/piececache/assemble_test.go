package piececache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
)

// The legacy response shape writeRecorded hashes: [before, fragment, after].
// The payload digest is the cached fragment's own SHA-256; the boundaries are
// hashed with SHA-256 of their bytes. These fixtures and the frozen digest
// constant below pin the formula the ENGINE-16 differential harness compares
// ETag headers against.
var (
	fixtureBefore  = []byte(`<!doctype html><html><body><main id="messages">`)
	fixturePayload = []byte(`<div class="message">hello</div><div class="message">world</div>`)
	fixtureAfter   = []byte(`</main></body></html>`)
)

const legacyFixtureDigestHex = "a8d4cfa2270b9cf4fec32b29035d2b062e916a6e629e470c9d71d38b4ad4e500"

// legacyETag replicates the digest loop of internal/web/recorded.go
// writeRecorded verbatim: little-endian uint64 length then digest per part,
// with the cached fragment digest standing in for the payload's own SHA-256.
func legacyETag(parts [][]byte, fragmentDigest [32]byte) [32]byte {
	hash := sha256.New()
	for i, part := range parts {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(part)))
		hash.Write(size[:])
		digest := fragmentDigest
		if i != 1 {
			digest = sha256.Sum256(part)
		}
		hash.Write(digest[:])
	}
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out
}

func fixturePieces(tb testing.TB) ([]*Entry, [][]byte) {
	tb.Helper()
	parts := [][]byte{fixtureBefore, fixturePayload, fixtureAfter}
	pieces := make([]*Entry, len(parts))
	for i, part := range parts {
		pieces[i] = NewEntry(part, mustMember(tb, part))
	}
	return pieces, parts
}

func TestAssembleETagMatchesLegacyRecordFormula(t *testing.T) {
	pieces, parts := fixturePieces(t)
	want := legacyETag(parts, sha256.Sum256(parts[1]))
	if got := fmt.Sprintf("%x", want); got != legacyFixtureDigestHex {
		t.Fatalf("fixture drifted: legacy replica = %s, constant = %s", got, legacyFixtureDigestHex)
	}

	for _, encoding := range []Encoding{Identity, Gzip} {
		_, etag, err := Assemble(nil, encoding, pieces...)
		if err != nil {
			t.Fatalf("%s: %v", encoding, err)
		}
		if etag != want {
			t.Fatalf("%s ETag = %x, want %x", encoding, etag, want)
		}
		// The legacy header spelling truncates to the low 16 bytes.
		header := fmt.Sprintf("W/\"%x\"", etag[:16])
		if header != `W/"a8d4cfa2270b9cf4fec32b29035d2b06"` {
			t.Fatalf("%s header = %s", encoding, header)
		}
	}
}

func TestAssembleGzipRoundTrip(t *testing.T) {
	rawParts := [][]byte{
		fixtureBefore,
		bytes.Repeat([]byte("The quick brown fox. "), 64),
		fixtureAfter,
	}
	pieces := make([]*Entry, len(rawParts))
	for i, part := range rawParts {
		pieces[i] = NewEntry(part, mustMember(t, part))
	}

	assembled, etag, err := Assemble(nil, Gzip, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(assembled, []byte{0x1f, 0x8b}) {
		t.Fatalf("assembled bytes do not start with the gzip magic: % x", assembled[:min(len(assembled), 2)])
	}
	if len(assembled) != len(pieces[0].Member)+len(pieces[1].Member)+len(pieces[2].Member) {
		t.Fatal("gzip assembly is not the concatenation of members")
	}
	want := bytes.Join(rawParts, nil)
	zr, err := gzip.NewReader(bytes.NewReader(assembled))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read of assembled bytes: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded %d bytes, want %d", len(got), len(want))
	}

	// Exactly three members, each decoding to its own part (Multistream(false)
	// walks one member at a time; default readers concatenate them).
	reader := bytes.NewReader(assembled)
	zr, err = gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	zr.Multistream(false)
	for i := 0; ; i++ {
		member, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("member %d read: %v", i, err)
		}
		if i >= len(rawParts) {
			t.Fatalf("found more than %d members", len(rawParts))
		}
		if !bytes.Equal(member, rawParts[i]) {
			t.Fatalf("member %d = %q, want %q", i, member, rawParts[i])
		}
		if err := zr.Reset(reader); err != nil {
			if err == io.EOF {
				if i != len(rawParts)-1 {
					t.Fatalf("assembled member count = %d, want %d", i+1, len(rawParts))
				}
				break
			}
			t.Fatalf("member %d reset: %v", i, err)
		}
		// Reset restores Multistream to true; re-pin one member at a time.
		zr.Multistream(false)
	}
	if etag != legacyETag(rawParts, sha256.Sum256(rawParts[1])) {
		t.Fatal("gzip ETag is not the raw identity-record digest")
	}
}

func TestAssembleIdentityConcatenatesRaw(t *testing.T) {
	raw := [][]byte{[]byte("one;"), nil, []byte("three")}
	pieces := make([]*Entry, len(raw))
	for i, part := range raw {
		pieces[i] = NewEntry(part, mustMember(t, part))
	}
	got, _, err := Assemble(nil, Identity, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("one;three"); !bytes.Equal(got, want) {
		t.Fatalf("Identity = %q, want %q", got, want)
	}
}

// TestAssembleAppendsToDst pins the append contract: bytes already in dst are
// kept, and callers reuse a response buffer with dst[:0].
func TestAssembleAppendsToDst(t *testing.T) {
	pieces := []*Entry{
		NewEntry([]byte("one"), mustMember(t, []byte("one"))),
		NewEntry([]byte("two"), mustMember(t, []byte("two"))),
	}
	prefix := make([]byte, 0, 64)
	prefix = append(prefix, "prefix:"...)
	out, first, err := Assemble(prefix, Identity, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("prefix:onetwo"); !bytes.Equal(out, want) {
		t.Fatalf("Assemble = %q, want %q", out, want)
	}
	if &out[0] != &prefix[0] {
		t.Fatal("Assemble copied instead of appending into the caller's buffer")
	}

	reused, second, err := Assemble(out[:0], Identity, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("onetwo"); !bytes.Equal(reused, want) {
		t.Fatalf("Assemble reuse = %q, want %q", reused, want)
	}
	if first != second {
		t.Fatal("identity records changed between assemblies")
	}
}

func TestAssembleEmptyIsError(t *testing.T) {
	dst := []byte("keep")
	out, etag, err := Assemble(dst, Gzip)
	if err == nil {
		t.Fatal("Assemble with no pieces succeeded, want error")
	}
	if !bytes.Equal(out, dst) {
		t.Fatalf("dst changed on error: %q", out)
	}
	if etag != ([32]byte{}) {
		t.Fatalf("ETag on error = %x, want zero", etag)
	}
}

func TestAssembleNilPieceIsError(t *testing.T) {
	good := NewEntry([]byte("x"), mustMember(t, []byte("x")))
	out, etag, err := Assemble([]byte("keep"), Identity, good, nil)
	if err == nil {
		t.Fatal("Assemble with a nil piece succeeded, want error")
	}
	if !bytes.Equal(out, []byte("keep")) {
		t.Fatalf("dst changed on error: %q", out)
	}
	if etag != ([32]byte{}) {
		t.Fatalf("ETag on error = %x, want zero", etag)
	}
}

// TestAssembleGzipRejectsRawOnlyPiece: a piece with raw bytes but no member
// must fail gzip assembly loudly rather than contributing an empty member and
// silently truncating the body. Identity assembly needs only Raw, so it still
// renders the piece; an empty raw with a nil member is a no-op under both.
func TestAssembleGzipRejectsRawOnlyPiece(t *testing.T) {
	rawOnly := NewEntry([]byte("raw only"), nil)
	tail := NewEntry([]byte("tail"), mustMember(t, []byte("tail")))

	dst := []byte("keep")
	out, etag, err := Assemble(dst, Gzip, rawOnly, tail)
	if err != errNoMember {
		t.Fatalf("Assemble(Gzip) err = %v, want errNoMember", err)
	}
	if !bytes.Equal(out, dst) {
		t.Fatalf("dst changed on error: %q", out)
	}
	if etag != ([32]byte{}) {
		t.Fatalf("ETag on error = %x, want zero", etag)
	}

	identity, _, err := Assemble(nil, Identity, rawOnly, tail)
	if err != nil {
		t.Fatalf("Assemble(Identity) with a raw-only piece: %v", err)
	}
	if want := []byte("raw onlytail"); !bytes.Equal(identity, want) {
		t.Fatalf("Identity = %q, want %q", identity, want)
	}

	empty := NewEntry(nil, nil)
	gzipOut, _, err := Assemble(nil, Gzip, empty, tail)
	if err != nil {
		t.Fatalf("Assemble(Gzip) with an empty piece: %v", err)
	}
	if !bytes.Equal(gzipOut, tail.Member) {
		t.Fatal("an empty piece changed the gzip assembly")
	}
	identityOut, _, err := Assemble(nil, Identity, empty, tail)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identityOut, []byte("tail")) {
		t.Fatalf("Identity = %q, want %q", identityOut, "tail")
	}
}

// TestETagOfMatchesAssembleAndValidates: the ETag-only entry point (304 path)
// returns exactly the digest Assemble returns and rejects the sets Assemble's
// gzip encoding rejects. Identity assembly is the documented exception for a
// raw-only piece, because raw bytes are all it needs.
func TestETagOfMatchesAssembleAndValidates(t *testing.T) {
	pieces, _ := fixturePieces(t)
	want, err := ETagOf(pieces...)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []Encoding{Identity, Gzip} {
		_, got, err := Assemble(nil, encoding, pieces...)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s ETag = %x, ETagOf = %x", encoding, got, want)
		}
	}

	good := pieces[0]
	if _, err := ETagOf(); err != errNoPieces {
		t.Fatalf("ETagOf() err = %v, want errNoPieces", err)
	}
	if _, err := ETagOf(good, nil); err != errNilPiece {
		t.Fatalf("ETagOf(nil piece) err = %v, want errNilPiece", err)
	}
	rawOnly := NewEntry([]byte("raw only"), nil)
	if _, err := ETagOf(rawOnly); err != errNoMember {
		t.Fatalf("ETagOf(raw-only) err = %v, want errNoMember", err)
	}
	if _, _, err := Assemble(nil, Gzip, rawOnly); err != errNoMember {
		t.Fatalf("Assemble(Gzip, raw-only) err = %v, want errNoMember", err)
	}
	if _, _, err := Assemble(nil, Identity, rawOnly); err != nil {
		t.Fatalf("Assemble(Identity, raw-only) err = %v, want nil", err)
	}
}

// TestETagStableAcrossDynamicPieces pins the engine's room-route ETag scheme:
// the validator covers the cache-stable pieces (shell + message list), not the
// per-request loadedAt piece. Legacy's room ETag moves on every request because
// loadedAt lives inside its hashed "before" part; exact parity would mean
// hashing ~100 KB per request, which the design forbids. The room-route
// differential therefore masks ETag as an intentional difference.
func TestETagStableAcrossDynamicPieces(t *testing.T) {
	cache := New(1 << 20)
	raws := [][]byte{
		[]byte("<!doctype html><html><body>"),
		[]byte(`<main id="messages">` + strings.Repeat("<div>message</div>", 32)),
		[]byte("</main></body></html>"),
	}
	keys := []string{"room/1/shell/v7", "room/1/messages/v42", "room/1/tail/v7"}
	cached := make([]*Entry, len(raws))
	for i, raw := range raws {
		entry, ok := cache.Put(keys[i], raw, mustMember(t, raw))
		if !ok {
			t.Fatalf("Put(%s) rejected", keys[i])
		}
		cached[i] = entry
	}

	loadedAt := func(stamp string) *Entry {
		raw := []byte(`<span data-loaded-at="` + stamp + `"></span>`)
		return NewEntry(raw, mustMember(t, raw))
	}
	scene := func(dynamic *Entry) []*Entry {
		return []*Entry{cached[0], dynamic, cached[1], cached[2]}
	}

	stable, err := ETagOf(cached...)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ETagOf(cached...)
	if err != nil {
		t.Fatal(err)
	}
	if stable != again {
		t.Fatal("ETagOf over cache-stable pieces is not stable")
	}

	body1, bodyTag1, err := Assemble(nil, Gzip, scene(loadedAt("2026-10-06T10:00:00Z"))...)
	if err != nil {
		t.Fatal(err)
	}
	body2, bodyTag2, err := Assemble(nil, Gzip, scene(loadedAt("2026-10-06T10:00:01Z"))...)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(body1, body2) {
		t.Fatal("fixture error: bodies must differ when loadedAt differs")
	}
	if bodyTag1 == bodyTag2 {
		t.Fatal("fixture error: Assemble's whole-body digest must move with loadedAt")
	}
	for i, body := range [][]byte{body1, body2} {
		decoded, err := decodeMember(body)
		if err != nil {
			t.Fatalf("scene %d: %v", i, err)
		}
		if !bytes.HasPrefix(decoded, raws[0]) || !bytes.HasSuffix(decoded, raws[2]) {
			t.Fatalf("scene %d decoded body lost a cache-stable piece", i)
		}
	}
}

func TestAssembleUnknownEncodingIsError(t *testing.T) {
	good := NewEntry([]byte("x"), mustMember(t, []byte("x")))
	if _, _, err := Assemble(nil, Encoding(99), good); err == nil {
		t.Fatal("Assemble with unknown encoding succeeded, want error")
	}
}

// TestAssembleNinePieces exercises the documented over-8-pieces path (one
// scratch allocation) and the ETag over nine records including an empty piece.
func TestAssembleNinePieces(t *testing.T) {
	pieces := make([]*Entry, 9)
	var want []byte
	for i := range pieces {
		var raw []byte
		if i != 4 {
			raw = []byte(fmt.Sprintf("part-%d;", i))
		}
		pieces[i] = NewEntry(raw, mustMember(t, raw))
		want = append(want, raw...)
	}
	out, etag, err := Assemble(nil, Identity, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("Identity = %q, want %q", out, want)
	}

	hash := sha256.New()
	for _, piece := range pieces {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(piece.Raw)))
		hash.Write(size[:])
		hash.Write(piece.Digest[:])
	}
	var wantTag [32]byte
	copy(wantTag[:], hash.Sum(nil))
	if etag != wantTag {
		t.Fatalf("ETag = %x, want %x", etag, wantTag)
	}
}

// TestAssembleGzipFromCache is the integration shape ENGINE-16 uses: Put both
// pieces, Get them back, assemble, and read the page with a gzip decoder.
func TestAssembleGzipFromCache(t *testing.T) {
	cache := New(1 << 20)
	raws := [][]byte{[]byte("<main>"), bytes.Repeat([]byte("message "), 100), []byte("</main>")}
	keys := []string{"shell/v1", "messages/v2", "tail/v1"}
	pieces := make([]*Entry, len(raws))
	for i := range raws {
		entry, ok := cache.Put(keys[i], raws[i], mustMember(t, raws[i]))
		if !ok {
			t.Fatalf("Put(%s) rejected", keys[i])
		}
		pieces[i] = entry
	}
	for i, entry := range pieces {
		if got := cache.Get(keys[i]); got != entry {
			t.Fatalf("Get(%s) did not return the stored entry", keys[i])
		}
	}
	assembled, _, err := Assemble(nil, Gzip, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMember(assembled)
	if err != nil {
		t.Fatal(err)
	}
	if want := bytes.Join(raws, nil); !bytes.Equal(got, want) {
		t.Fatalf("assembled page = %.40q..., want %.40q...", got, want)
	}
}
