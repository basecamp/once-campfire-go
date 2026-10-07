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
	// Gzip is the RFC 1952 multi-member form: each piece's complete Member
	// concatenated. Every deployed decoder — net/http's gzip.Reader included —
	// decodes concatenated members as the concatenation of their raw streams,
	// so no deflate-window or CRC bookkeeping crosses the piece boundary.
	Gzip
	// Zstd is the multi-frame zstd form: each piece's complete Zstd frame
	// concatenated. zstd frames are independent, so a decoder processes
	// concatenated frames as the concatenation of their raw streams exactly
	// like gzip multi-member streams.
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
	errNoMember = errors.New("piececache: gzip piece has raw bytes but no member")
	errEncoding = errors.New("piececache: unknown encoding")
)

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
// path is gated at zero allocations.
//
// An empty piece list, a nil piece, an unknown encoding, or a piece lacking
// the member for a compressed encoding (raw bytes but no gzip member under
// Gzip, no zstd frame under Zstd) returns an error and leaves dst unchanged.
// (Empty lists are rejected rather than assembled to zero bytes: a response
// assembled from nothing is a routing bug, and a silent empty 200 would hide
// it. A raw-only piece under a compressed encoding would silently drop its
// content, so it is rejected rather than assembled without it; identity
// assembly can still render it.)
func Assemble(dst []byte, encoding Encoding, pieces ...*Entry) ([]byte, [32]byte, error) {
	if len(pieces) == 0 {
		return dst, [32]byte{}, errNoPieces
	}
	total := 0
	for _, piece := range pieces {
		if piece == nil {
			return dst, [32]byte{}, errNilPiece
		}
		switch encoding {
		case Identity:
			total += len(piece.Raw)
		case Gzip:
			if len(piece.Raw) > 0 && len(piece.Member) == 0 {
				return dst, [32]byte{}, errNoMember
			}
			total += len(piece.Member)
		case Zstd:
			if len(piece.Raw) > 0 && len(piece.Zstd) == 0 {
				return dst, [32]byte{}, errNoMember
			}
			total += len(piece.Zstd)
		default:
			return dst, [32]byte{}, errEncoding
		}
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
		for _, piece := range pieces {
			dst = append(dst, piece.Member...)
		}
	default: // Zstd, checked above.
		for _, piece := range pieces {
			dst = append(dst, piece.Zstd...)
		}
	}
	return dst, etag, nil
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
// pieces are errors, and so is a piece with raw bytes but no gzip member
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
		if len(piece.Raw) > 0 && len(piece.Member) == 0 {
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
