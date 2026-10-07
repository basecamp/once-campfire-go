// Package responsebody identifies completed response bytes independently of HTTP
// validators. Cached parts carry their own strong digest, so assembling a body
// hashes only part lengths and digests, not the full payload again.
package responsebody

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
)

// Part owns bytes that must remain unchanged until all writes finish. NewPart
// transfers that ownership; callers must not mutate or recycle the input early.
// No accessor exposes writable bytes or permits supplying an unchecked digest.
type Part struct {
	data   []byte
	digest [32]byte
}

func NewPart(data []byte) Part { return Part{data: data, digest: sha256.Sum256(data)} }
func (p Part) Len() int        { return len(p.data) }
func (p Part) Digest() [32]byte {
	if len(p.data) == 0 {
		return sha256.Sum256(nil) // the zero value is also a valid empty part
	}
	return p.digest
}
func (p Part) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(p.data)
	if err == nil && n != len(p.data) {
		err = io.ErrShortWrite
	}
	return int64(n), err
}

// Digest identifies the exact ordered parts, including their boundaries. Two
// decompositions of the same bytes may have different identities; neither can
// alias different bytes without a SHA-256 collision. This matches recorded-body
// validator construction, but retains all 256 bits for internal compression reuse.
func Digest(parts []Part) [32]byte {
	hash := sha256.New()
	for _, part := range parts {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(part.Len()))
		hash.Write(size[:])
		digest := part.Digest()
		hash.Write(digest[:])
	}
	var digest [32]byte
	hash.Sum(digest[:0])
	return digest
}
