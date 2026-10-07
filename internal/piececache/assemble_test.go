package piececache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"os/exec"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/zstd"
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
		pieces[i] = NewEntry(part, mustFragment(tb, part), nil)
	}
	return pieces, parts
}

func mustZstdFrame(tb testing.TB, src []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriterLevel(&buf, 3)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := w.Write(src); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

// decodeZstdFrames decodes a multi-frame zstd stream with the system zstd
// CLI, the same decoder the zstd package's own tests use.
func decodeZstdFrames(tb testing.TB, frames []byte) ([]byte, error) {
	tb.Helper()
	command := exec.Command("zstd", "-d", "-q", "-c")
	command.Stdin = bytes.NewReader(frames)
	decoded, err := command.Output()
	if err != nil {
		return nil, err
	}
	return decoded, nil
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

func TestAssembleZstdRoundTrip(t *testing.T) {
	rawParts := [][]byte{
		fixtureBefore,
		bytes.Repeat([]byte("The quick brown fox. "), 64),
		fixtureAfter,
	}
	pieces := make([]*Entry, len(rawParts))
	for i, part := range rawParts {
		pieces[i] = NewEntry(part, mustFragment(t, part), mustZstdFrame(t, part))
	}

	want := legacyETag(rawParts, sha256.Sum256(rawParts[1]))
	for _, encoding := range []Encoding{Identity, Gzip, Zstd} {
		assembled, etag, err := Assemble(nil, encoding, pieces...)
		if err != nil {
			t.Fatalf("%s: %v", encoding, err)
		}
		if etag != want {
			t.Fatalf("%s ETag = %x, want %x", encoding, etag, want)
		}
		if encoding == Zstd {
			decoded, err := decodeZstdFrames(t, assembled)
			if err != nil {
				t.Fatalf("zstd decode: %v", err)
			}
			wantRaw := append(append([]byte{}, rawParts[0]...), rawParts[1]...)
			wantRaw = append(wantRaw, rawParts[2]...)
			if !bytes.Equal(decoded, wantRaw) {
				t.Fatalf("zstd round trip: decoded %d bytes, want %d", len(decoded), len(wantRaw))
			}
		}
	}

	// A zstd-less piece under Zstd is an error, like a gzip-less piece under
	// Gzip; identity still renders it.
	rawOnly := NewEntry([]byte("raw only"), nil, nil)
	if _, _, err := Assemble(nil, Zstd, rawOnly); err != errNoMember {
		t.Fatalf("Assemble(Zstd, raw-only) err = %v, want errNoMember", err)
	}
	if _, _, err := Assemble(nil, Zstd, pieces[0], rawOnly); err != errNoMember {
		t.Fatalf("Assemble(Zstd, mixed) err = %v, want errNoMember", err)
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
		pieces[i] = NewEntry(part, mustFragment(t, part), nil)
	}

	assembled, etag, err := Assemble(nil, Gzip, pieces...)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(assembled, []byte{0x1f, 0x8b}) {
		t.Fatalf("assembled bytes do not start with the gzip magic: % x", assembled[:min(len(assembled), 2)])
	}
	if len(assembled) != 10+len(pieces[0].Fragment)+len(pieces[1].Fragment)+len(pieces[2].Fragment)+5+8 {
		t.Fatalf("gzip assembly length %d, want header+fragments+final block+trailer (%d)", len(assembled), 10+len(pieces[0].Fragment)+len(pieces[1].Fragment)+len(pieces[2].Fragment)+5+8)
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

	// The trailer carries the CRC-32 and ISIZE of the whole raw
	// concatenation, and every byte after the fragments belongs to this one
	// member: the final empty stored block (01 00 00 ff ff) followed by the
	// 8-byte trailer.
	trailer := assembled[len(assembled)-8:]
	if crc := binary.LittleEndian.Uint32(trailer[0:4]); crc != crc32.ChecksumIEEE(want) {
		t.Fatalf("trailer CRC = %08x, want %08x", crc, crc32.ChecksumIEEE(want))
	}
	if size := binary.LittleEndian.Uint32(trailer[4:8]); size != uint32(len(want)) {
		t.Fatalf("trailer ISIZE = %d, want %d", size, len(want))
	}
	final := assembled[10+len(pieces[0].Fragment)+len(pieces[1].Fragment)+len(pieces[2].Fragment) : len(assembled)-8]
	if !bytes.Equal(final, []byte{0x01, 0x00, 0x00, 0xff, 0xff}) {
		t.Fatalf("final block = % x, want the empty stored final block", final)
	}
	if etag != legacyETag(rawParts, sha256.Sum256(rawParts[1])) {
		t.Fatal("gzip ETag is not the raw identity-record digest")
	}
}

// TestAssembleGzipSingleMember is the browser-safety contract: the assembled
// body must be exactly one gzip member. Chromium decodes only the first
// member of a multi-member stream, so a body with trailing bytes after the
// first member would render the shell without its messages. Multistream(false)
// stops after the first member, and with a bytes.Reader the reader is left
// exactly at the end of the stream, so zero remaining bytes is the
// single-member proof.
func TestAssembleGzipSingleMember(t *testing.T) {
	rawParts := [][]byte{
		[]byte("<!doctype html><html><body><main>"),
		bytes.Repeat([]byte(`<div class="message">hello</div>`), 64),
		[]byte("</main></body></html>"),
	}
	pieces := make([]*Entry, len(rawParts))
	for i, part := range rawParts {
		pieces[i] = NewEntry(part, mustFragment(t, part), nil)
	}
	assembled, _, err := Assemble(nil, Gzip, pieces...)
	if err != nil {
		t.Fatal(err)
	}

	reader := bytes.NewReader(assembled)
	zr, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	zr.Multistream(false)
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("single-member read: %v", err)
	}
	if want := bytes.Join(rawParts, nil); !bytes.Equal(got, want) {
		t.Fatalf("single-member decode = %d bytes, want %d", len(got), len(want))
	}
	if remaining := reader.Len(); remaining != 0 {
		t.Fatalf("assembled body has %d trailing bytes after member 1: more than one gzip member", remaining)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}

	// Negative control: a two-member stream must fail the same check, so the
	// test above really does catch the multi-member wire form.
	member0, _, err := Assemble(nil, Gzip, pieces[0])
	if err != nil {
		t.Fatal(err)
	}
	member1, _, err := Assemble(nil, Gzip, pieces[1])
	if err != nil {
		t.Fatal(err)
	}
	control := append(append([]byte{}, member0...), member1...)
	reader = bytes.NewReader(control)
	zr, err = gzip.NewReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	zr.Multistream(false)
	if _, err := io.ReadAll(zr); err != nil {
		t.Fatal(err)
	}
	if remaining := reader.Len(); remaining == 0 {
		t.Fatal("negative control failed: a two-member stream left zero bytes, the check is not sensitive")
	}
}

func TestAssembleIdentityConcatenatesRaw(t *testing.T) {
	raw := [][]byte{[]byte("one;"), nil, []byte("three")}
	pieces := make([]*Entry, len(raw))
	for i, part := range raw {
		pieces[i] = NewEntry(part, mustFragment(t, part), nil)
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
		NewEntry([]byte("one"), mustFragment(t, []byte("one")), nil),
		NewEntry([]byte("two"), mustFragment(t, []byte("two")), nil),
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
	good := NewEntry([]byte("x"), mustFragment(t, []byte("x")), nil)
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
	rawOnly := NewEntry([]byte("raw only"), nil, nil)
	tail := NewEntry([]byte("tail"), mustFragment(t, []byte("tail")), nil)

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

	empty := NewEntry(nil, nil, nil)
	gzipOut, _, err := Assemble(nil, Gzip, empty, tail)
	if err != nil {
		t.Fatalf("Assemble(Gzip) with an empty piece: %v", err)
	}
	// The empty piece contributes no bytes; the member decodes to "tail" and
	// is still exactly one member.
	decoded, err := decodeMember(gzipOut)
	if err != nil {
		t.Fatalf("Assemble(Gzip, empty, tail) does not decode: %v", err)
	}
	if want := []byte("tail"); !bytes.Equal(decoded, want) {
		t.Fatalf("Assemble(Gzip, empty, tail) = %q, want %q", decoded, want)
	}
	identityOut, _, err := Assemble(nil, Identity, empty, tail)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(identityOut, []byte("tail")) {
		t.Fatalf("Identity = %q, want %q", identityOut, "tail")
	}
}

// TestCrc32Combine pins the trailer's CRC combination against crc32.Update
// over random data and random partitions, plus the sequential chain form the
// assembly uses and the fixed edge lengths (powers of two, byte boundaries).
// The assembly must not re-scan raw bytes, so the combine is the only source
// of the whole-body CRC.
func TestCrc32Combine(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	crc := func(data []byte) uint32 { return crc32.ChecksumIEEE(data) }
	combine := func(crc1, crc2 uint32, len2 int) uint32 {
		return crc32Combine(crc1, crc2, len2)
	}

	for iter := 0; iter < 2000; iter++ {
		n := rng.Intn(64 << 10)
		cut := rng.Intn(n + 1)
		data := make([]byte, n)
		rng.Read(data)
		got := combine(crc(data[:cut]), crc(data[cut:]), n-cut)
		if want := crc(data); got != want {
			t.Fatalf("partition n=%d cut=%d: combine = %08x, want %08x", n, cut, got, want)
		}
	}
	// The assembly chain: combine the running CRC with each piece's CRC and
	// length in order.
	for iter := 0; iter < 500; iter++ {
		var data []byte
		got := uint32(0)
		for p := 0; p < 1+rng.Intn(6); p++ {
			chunk := make([]byte, rng.Intn(5000))
			rng.Read(chunk)
			got = combine(got, crc(chunk), len(chunk))
			data = append(data, chunk...)
		}
		if want := crc(data); got != want {
			t.Fatalf("chain iter=%d: combine = %08x, want %08x", iter, got, want)
		}
	}
	for _, n := range []int{0, 1, 2, 3, 7, 8, 15, 16, 31, 32, 33, 128, 255, 256, 257, 1024, 65535, 65536, 1 << 20, 1<<20 + 7, 1 << 24} {
		data := make([]byte, n)
		rng.Read(data)
		for _, cut := range []int{0, 1, n / 2, n - 1, n} {
			if cut < 0 || cut > n {
				continue
			}
			got := combine(crc(data[:cut]), crc(data[cut:]), n-cut)
			if want := crc(data); got != want {
				t.Fatalf("edge n=%d cut=%d: combine = %08x, want %08x", n, cut, got, want)
			}
		}
	}
	// The fill-time zero-operator path the assembly actually uses: combining
	// with the stored op must equal the len-based combine for the same
	// lengths (a full x2nmodp per piece at assembly would be ~8x slower, so
	// NewEntry stores ZerosOp per entry).
	for iter := 0; iter < 1000; iter++ {
		n := rng.Intn(1 << 18)
		cut := rng.Intn(n + 1)
		data := make([]byte, n)
		rng.Read(data)
		op := ZerosOp(n - cut)
		got := multmodp(op, crc(data[:cut])) ^ crc(data[cut:])
		if want := crc(data); got != want {
			t.Fatalf("op path n=%d cut=%d: combine = %08x, want %08x", n, cut, got, want)
		}
		if entry := NewEntry(data, nil, nil); entry.ZerosOp != ZerosOp(len(data)) {
			t.Fatalf("NewEntry ZerosOp = %08x, want %08x", entry.ZerosOp, ZerosOp(len(data)))
		}
	}
	// The degenerate cases the assembly relies on for empty pieces.
	if got := combine(0x12345678, 0x9abcdef0, 0); got != 0x12345678 {
		t.Fatalf("combine(len2=0) = %08x, want crc1", got)
	}
	if got := combine(0, 0, 0); got != 0 {
		t.Fatalf("combine(0,0,0) = %08x, want 0", got)
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
	rawOnly := NewEntry([]byte("raw only"), nil, nil)
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
		entry, ok := cache.Put(keys[i], raw, mustFragment(t, raw))
		if !ok {
			t.Fatalf("Put(%s) rejected", keys[i])
		}
		cached[i] = entry
	}

	loadedAt := func(stamp string) *Entry {
		raw := []byte(`<span data-loaded-at="` + stamp + `"></span>`)
		return NewEntry(raw, mustFragment(t, raw), nil)
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
	good := NewEntry([]byte("x"), mustFragment(t, []byte("x")), nil)
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
		pieces[i] = NewEntry(raw, mustFragment(t, raw), nil)
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
		entry, ok := cache.Put(keys[i], raws[i], mustFragment(t, raws[i]))
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
