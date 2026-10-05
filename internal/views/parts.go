package views

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
)

// The page parts and ETag of the reference's kit (crates/kit/src/deflater/splice.rs): a page made
// mostly of cached fragments is split into parts at its large fragments, and its ETag is a hash of
// the parts' digests rather than of the whole body. A fragment's digest is remembered with the
// fragment; a text's (the layout around the fragments) by the text's bytes.
const (
	// Smaller fragments aren't worth a part of their own; they stay in the text around them.
	minFragment = 1024
	// At most this much text between two fragments travels with the second; more is a text part.
	maxGlue = 256
	// A bound on the bytes of texts remembered with their SHA-256.
	maxTextBytes = 16 << 20
	// Larger texts are hashed every time.
	maxStoredText = maxTextBytes / 64
	// What each stored entry costs beyond its bytes.
	entryOverhead = 128
)

// ETag is Rack::ETag's weak validator for the page's body: 32 hex digits of a SHA-256 over its
// parts when it has fragments large enough for parts of their own, else of the whole body. It
// returns "" for an empty body, which Rack::ETag doesn't tag.
func (p *RecordedPage) ETag() string {
	gather := gathered{}
	type run struct {
		gap      []byte
		fragment *Fragment
	}
	var runs []run
	position := 0
	for _, placed := range p.Fragments {
		gather.push(p.Text[position:placed.Offset])
		position = placed.Offset
		if len(placed.Fragment.HTML) < minFragment {
			gather.push([]byte(placed.Fragment.HTML))
		} else {
			runs = append(runs, run{gather.take(), placed.Fragment})
		}
	}
	gather.push(p.Text[position:])
	rest := gather.take()
	if len(runs) == 0 {
		if len(rest) == 0 {
			return ""
		}
		sum := sha256.Sum256(rest)
		return hex.EncodeToString(sum[:16])
	}
	hash := sha256.New()
	var size [8]byte
	text := func(t []byte) {
		sha := textSHA(t)
		hash.Write([]byte{'T'})
		binary.LittleEndian.PutUint64(size[:], uint64(len(t)))
		hash.Write(size[:])
		hash.Write(sha[:])
	}
	followsFragment := false
	for _, r := range runs {
		var glue []byte
		if followsFragment && len(r.gap) <= maxGlue {
			glue = r.gap
		} else if len(r.gap) > 0 {
			text(r.gap)
		}
		sha := r.fragment.Digest()
		hash.Write([]byte{'F'})
		binary.LittleEndian.PutUint64(size[:], uint64(len(glue)))
		hash.Write(size[:])
		hash.Write(glue)
		binary.LittleEndian.PutUint64(size[:], uint64(len(r.fragment.HTML)))
		hash.Write(size[:])
		hash.Write(sha[:])
		followsFragment = true
	}
	if len(rest) > 0 {
		text(rest)
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// gathered joins body bytes gathered piece by piece only when there's more than one piece.
type gathered struct {
	pieces [][]byte
}

func (g *gathered) push(b []byte) {
	if len(b) > 0 {
		g.pieces = append(g.pieces, b)
	}
}

func (g *gathered) take() []byte {
	var joined []byte
	switch len(g.pieces) {
	case 0:
	case 1:
		joined = g.pieces[0]
	default:
		for _, piece := range g.pieces {
			joined = append(joined, piece...)
		}
	}
	g.pieces = g.pieces[:0]
	return joined
}

// generations is a map in two generations, bounded by what its entries cost: reading an old entry
// promotes it, and when the young generation costs more than half the budget it becomes the old
// one (dropping the previous old one).
type generations struct {
	mu        sync.Mutex
	young     map[string][32]byte
	old       map[string][32]byte
	youngCost int
	budget    int
}

var textSHAs = &generations{young: map[string][32]byte{}, old: map[string][32]byte{}, budget: maxTextBytes}

// textSHA is the SHA-256 of text, remembered by its bytes: a page repeats its texts from one
// request to the next, and finding one again costs a hash and a compare.
func textSHA(text []byte) [32]byte {
	g := textSHAs
	g.mu.Lock()
	if sha, ok := g.young[string(text)]; ok {
		g.mu.Unlock()
		return sha
	}
	if sha, ok := g.old[string(text)]; ok {
		delete(g.old, string(text))
		g.insert(string(text), sha)
		g.mu.Unlock()
		return sha
	}
	g.mu.Unlock()
	sha := sha256.Sum256(text)
	if len(text) <= maxStoredText {
		g.mu.Lock()
		g.insert(string(text), sha)
		g.mu.Unlock()
	}
	return sha
}

func (g *generations) insert(key string, sha [32]byte) {
	if _, ok := g.young[key]; !ok {
		g.youngCost += len(key) + entryOverhead
	}
	g.young[key] = sha
	if g.youngCost > g.budget/2 {
		g.old = g.young
		g.young = map[string][32]byte{}
		g.youngCost = 0
	}
}
