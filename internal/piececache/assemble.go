package piececache

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Encoding selects the wire form Assemble produces. The zero value is
// Identity.
type Encoding uint8

const (
	// Identity is the uncompressed form: each piece's Raw bytes.
	Identity Encoding = iota
	// Gzip is ONE RFC 1952 gzip member: the header, each piece's Fragment
	// (a raw deflate block-stream ended at a byte-aligned non-final boundary)
	// concatenated, a final empty stored block, and the CRC32/ISIZE trailer
	// of the whole raw concatenation. The per-piece CRCs are stored at fill
	// time and combined with the GF(2) matrix method, so assembling the
	// trailer scans no raw bytes. A multi-member stream would be legal
	// RFC 1952, and Go/Python/curl/loadgen decode it — but Chromium decodes
	// only the first member, so the splice is the browser-safe form.
	Gzip
	// Zstd is the multi-frame zstd form: each piece's complete Zstd frame
	// concatenated. zstd frames are independent, so a decoder processes
	// concatenated frames as the concatenation of their raw streams exactly
	// like gzip multi-member streams. Chromium likewise decodes only the
	// first frame of a multi-frame stream (the same probe that broke gzip
	// multi-member), so internal/web serves zstd only when a response is a
	// single frame; the assembly here remains for non-browser clients and
	// for the multi-frame test corpus.
	Zstd
)

func (e Encoding) String() string {
	switch e {
	case Identity:
		return "identity"
	case Gzip:
		return "gzip"
	case Zstd:
		return "zstd"
	default:
		return "unknown"
	}
}

var (
	errNoPieces = errors.New("piececache: assemble with no pieces")
	errNilPiece = errors.New("piececache: assemble with a nil piece")
	errNoMember = errors.New("piececache: gzip piece has raw bytes but no deflate fragment")
	errEncoding = errors.New("piececache: unknown encoding")
)

// gzipHeader is the fixed 10-byte RFC 1952 member header the splice emits:
// magic 1f 8b, method 8 (deflate), FLG 0, MTIME 0, XFL 0, OS 3 (the OS the
// compress/gzip fill used; XFL/OS are advisory and ignored by decoders).
var gzipHeader = [10]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3}

// gzipFinalBlock ends the member's deflate stream: an empty stored block with
// BFINAL=1 (LEN 0, NLEN 0xffff), byte-aligned after the last flushed
// fragment. Without it the stream would have no final block and the CRC/ISIZE
// trailer would not terminate the member.
var gzipFinalBlock = [5]byte{0x01, 0x00, 0x00, 0xff, 0xff}

// gzipSpliceOverhead is the fixed member bytes around the fragments: header
// (10) + final empty stored block (5) + CRC32 and ISIZE trailer (8).
const gzipSpliceOverhead = 10 + 5 + 8

// Assemble appends the encoded pieces to dst and returns the extended slice
// plus the ETag digest. Bytes already in dst are kept: assembly starts at
// len(dst), and callers reuse one response buffer with dst[:0] (or keep the
// returned slice and reslice it), which makes the operation allocation-free
// whenever dst has room for the encoded pieces.
//
// The ETag is computed from piece identity records, never from the encoded
// body: for each piece in order, little-endian uint64(raw length) followed by
// SHA-256(raw). That is the digest loop of internal/web/recorded.go's
// writeRecorded byte for byte, so a differential harness can compare ETag
// headers directly. Because the records cover raw content, Identity, Gzip and
// Zstd yield the same digest for the same pieces. The legacy header spelling
// is W/"<hex of the low 16 bytes>"; this returns the full 32-byte digest so a
// caller can also use it for 304 validation.
//
// The digest returned covers exactly the pieces passed. When the body also
// contains per-request dynamic pieces (loadedAt), take the response validator
// from ETagOf over the cache-stable subset instead — see ETagOf for the
// engine's room-route ETag semantics.
//
// The digest pass is allocation-free for up to 8 pieces, covering the response
// model's ~3-piece pages, by hashing a fixed 8×40-byte stack scratch. More than
// 8 pieces uses a per-call scratch slice sized to the piece count; only the ≤8
// path is gated at zero allocations. The gzip trailer's CRC combination is
// allocation-free at any piece count (stack matrices and no raw re-scan).
//
// An empty piece list, a nil piece, an unknown encoding, or a piece lacking
// the fragment for a compressed encoding (raw bytes but no deflate fragment
// under Gzip, no zstd frame under Zstd) returns an error and leaves dst
// unchanged. (Empty lists are rejected rather than assembled to zero bytes: a
// response assembled from nothing is a routing bug, and a silent empty 200
// would hide it. A raw-only piece under a compressed encoding would silently
// drop its content, so it is rejected rather than assembled without it;
// identity assembly can still render it.)
func Assemble(dst []byte, encoding Encoding, pieces ...*Entry) ([]byte, [32]byte, error) {
	total, err := encodedLen(encoding, pieces)
	if err != nil {
		return dst, [32]byte{}, err
	}
	etag := etagOf(pieces)

	// Grow once, explicitly: a warm caller buffer passes through with no
	// allocation, and a cold one pays exactly one allocation rather than
	// append's geometric regrowth across pieces.
	if free := cap(dst) - len(dst); free < total {
		grown := make([]byte, len(dst), len(dst)+total)
		copy(grown, dst)
		dst = grown
	}
	switch encoding {
	case Identity:
		for _, piece := range pieces {
			dst = append(dst, piece.Raw...)
		}
	case Gzip:
		dst = appendGzipMember(dst, pieces)
	default: // Zstd, checked above.
		for _, piece := range pieces {
			dst = append(dst, piece.Zstd...)
		}
	}
	return dst, etag, nil
}

// EncodedLen returns the exact number of bytes Assemble would append for
// encoding and pieces, with Assemble's validation (empty list, nil piece,
// raw-only piece under a compressed encoding, unknown encoding). It shares its
// size computation with Assemble, so a caller that carves a buffer of exactly
// EncodedLen bytes can hand it to Assemble knowing no growth (and no write
// past a carve) will happen. Like Assemble it is allocation-free.
func EncodedLen(encoding Encoding, pieces ...*Entry) (int, error) {
	return encodedLen(encoding, pieces)
}

func encodedLen(encoding Encoding, pieces []*Entry) (int, error) {
	if len(pieces) == 0 {
		return 0, errNoPieces
	}
	total := 0
	for _, piece := range pieces {
		if piece == nil {
			return 0, errNilPiece
		}
		switch encoding {
		case Identity:
			total += len(piece.Raw)
		case Gzip:
			if len(piece.Raw) > 0 && len(piece.Fragment) == 0 {
				return 0, errNoMember
			}
			total += len(piece.Fragment)
		case Zstd:
			if len(piece.Raw) > 0 && len(piece.Zstd) == 0 {
				return 0, errNoMember
			}
			total += len(piece.Zstd)
		default:
			return 0, errEncoding
		}
	}
	if encoding == Gzip {
		total += gzipSpliceOverhead
	}
	return total, nil
}

// appendGzipMember appends one complete gzip member for pieces to dst:
// header, concatenated fragments, final empty stored block, then the CRC32
// and ISIZE of the entire raw concatenation. The CRC is combined from the
// per-piece stored CRCs and zero operators (crc32Combine), so no raw byte is
// scanned; len(Raw) is the per-piece ISIZE operand. Called only after
// encodedLen validated the pieces.
func appendGzipMember(dst []byte, pieces []*Entry) []byte {
	dst = append(dst, gzipHeader[:]...)
	for _, piece := range pieces {
		dst = append(dst, piece.Fragment...)
	}
	dst = append(dst, gzipFinalBlock[:]...)
	crc := uint32(0)
	var size uint32
	for _, piece := range pieces {
		crc = multmodp(piece.ZerosOp, crc) ^ piece.CRC
		size += uint32(len(piece.Raw))
	}
	var trailer [8]byte
	binary.LittleEndian.PutUint32(trailer[0:4], crc)
	binary.LittleEndian.PutUint32(trailer[4:8], size)
	return append(dst, trailer[:]...)
}

// crc32Combine returns the CRC-32 (IEEE, zlib convention with the final XOR
// included, as crc32.ChecksumIEEE produces) of the data covered by crc1
// followed by the len2 bytes covered by crc2, without touching any data. It
// is zlib's crc32_combine: crc1 is multiplied by x^(8·len2) modulo the
// reflected CRC-32 polynomial in GF(2), then XORed with crc2. The assembly
// hot path uses the fill-time equivalent: each entry carries its own ZerosOp,
// so appending a piece's CRC costs one multmodp (a full x2nmodp per request
// would be ~8x slower); crc32Combine remains the tested canonical form for
// the property tests. Both are allocation-free.
func crc32Combine(crc1, crc2 uint32, len2 int) uint32 {
	if len2 <= 0 {
		return crc1
	}
	return multmodp(x2nmodp(int64(len2), 3), crc1) ^ crc2
}

// crc32Poly is the reflected CRC-32 polynomial, zlib's POLY.
const crc32Poly = 0xedb88320

// x2nTable[i] is x^(2^i) modulo the polynomial, in the reflected
// representation, computed once at package init.
var x2nTable [32]uint32

func init() {
	p := uint32(1) << 30 // x^1
	x2nTable[0] = p
	for n := 1; n < 32; n++ {
		p = multmodp(p, p)
		x2nTable[n] = p
	}
}

// multmodp multiplies a by b modulo the reflected CRC-32 polynomial, the
// GF(2) multiply zlib's crc32_combine uses: a's bits select b (which is
// multiplied by x on every step), most significant first. Like zlib, the
// loop stops once b has been folded past a's lowest set bit (a's lower bits
// are then all zero), so sparse operators — the x^(2^k) table entries and
// the computed x^(8·len2) — pay only their significant bits.
func multmodp(a, b uint32) uint32 {
	var p uint32
	m := uint32(1) << 31
	for m != 0 {
		if a&m != 0 {
			p ^= b
			if a&(m-1) == 0 {
				break
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = (b >> 1) ^ crc32Poly
		} else {
			b >>= 1
		}
	}
	return p
}

// x2nmodp returns x^(n·2^k) modulo the polynomial.
func x2nmodp(n int64, k uint) uint32 {
	p := uint32(1) << 31 // x^0
	for n != 0 {
		if n&1 != 0 {
			p = multmodp(x2nTable[k&31], p)
		}
		n >>= 1
		k++
	}
	return p
}

// ETagOf returns the identity-record ETag digest for an ordered piece list
// without assembling a body. It is the 304 path: a caller probes cache keys,
// computes ETagOf over the cache-stable pieces it found, compares the digest
// to If-None-Match, and only assembles when the validator does not match.
//
// The engine's ETag scheme covers cache-stable pieces only. Legacy's room ETag
// moves on every request because loadedAt sits inside its hashed "before"
// part; reproducing that would mean hashing roughly 100 KB per request, which
// the design forbids. Callers therefore pass the pieces a version key
// identifies — the room shell and message list — and exclude per-request
// dynamic pieces (loadedAt); the room-route differential masks ETag as an
// intentional difference. See Assemble for the full record formula.
//
// Validation is the common subset of Assemble's checks: an empty list and nil
// pieces are errors, and so is a piece with raw bytes but no deflate fragment
// (Assemble's gzip encoding rejects that set; identity assembly is the one
// path that can still render a raw-only piece).
func ETagOf(pieces ...*Entry) ([32]byte, error) {
	if len(pieces) == 0 {
		return [32]byte{}, errNoPieces
	}
	for _, piece := range pieces {
		if piece == nil {
			return [32]byte{}, errNilPiece
		}
		if len(piece.Raw) > 0 && len(piece.Fragment) == 0 {
			return [32]byte{}, errNoMember
		}
	}
	return etagOf(pieces), nil
}

// etagOf hashes the identity records: per piece, LE64(len(Raw)) then Digest.
// The ≤8 branch keeps its scratch on the stack; the >8 branch uses a per-call
// slice.
func etagOf(pieces []*Entry) [32]byte {
	if len(pieces) <= 8 {
		var scratch [8 * 40]byte
		offset := 0
		for _, piece := range pieces {
			binary.LittleEndian.PutUint64(scratch[offset:], uint64(len(piece.Raw)))
			copy(scratch[offset+8:], piece.Digest[:])
			offset += 40
		}
		return sha256.Sum256(scratch[:offset])
	}
	records := make([]byte, len(pieces)*40)
	offset := 0
	for _, piece := range pieces {
		binary.LittleEndian.PutUint64(records[offset:], uint64(len(piece.Raw)))
		copy(records[offset+8:], piece.Digest[:])
		offset += 40
	}
	return sha256.Sum256(records)
}
