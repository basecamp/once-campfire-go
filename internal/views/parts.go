package views

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"unsafe"
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
// returns "" for an empty body, which Rack::ETag doesn't tag. The parts' records go into one buffer
// hashed once, which reuses its buffers from one page to the next.
func (p *RecordedPage) ETag() string {
	parts := false
	for _, placed := range p.Fragments {
		if len(placed.Fragment.HTML) >= minFragment {
			parts = true
			break
		}
	}
	scratch := etagScratches.Get().(*etagScratch)
	defer scratch.release()
	gather := &scratch.gather
	position := 0
	if !parts {
		for _, placed := range p.Fragments {
			gather.push(p.Text[position:placed.Offset])
			gather.push(unsafe.Slice(unsafe.StringData(placed.Fragment.HTML), len(placed.Fragment.HTML)))
			position = placed.Offset
		}
		gather.push(p.Text[position:])
		body := gather.take()
		if len(body) == 0 {
			return ""
		}
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:16])
	}
	input := scratch.input[:0]
	followsFragment := false
	for _, placed := range p.Fragments {
		gather.push(p.Text[position:placed.Offset])
		position = placed.Offset
		if len(placed.Fragment.HTML) < minFragment {
			gather.push(unsafe.Slice(unsafe.StringData(placed.Fragment.HTML), len(placed.Fragment.HTML)))
			continue
		}
		gap := gather.take()
		var glue []byte
		if followsFragment && len(gap) <= maxGlue {
			glue = gap
		} else if len(gap) > 0 {
			input = appendTextRecord(input, gap)
		}
		sha := placed.Fragment.Digest()
		input = append(input, 'F')
		input = binary.LittleEndian.AppendUint64(input, uint64(len(glue)))
		input = append(input, glue...)
		input = binary.LittleEndian.AppendUint64(input, uint64(len(placed.Fragment.HTML)))
		input = append(input, sha[:]...)
		followsFragment = true
	}
	gather.push(p.Text[position:])
	if rest := gather.take(); len(rest) > 0 {
		input = appendTextRecord(input, rest)
	}
	scratch.input = input
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:16])
}

// appendTextRecord appends a text part's record: T, its length and its SHA-256.
func appendTextRecord(input, text []byte) []byte {
	sha := textSHA(text)
	input = append(input, 'T')
	input = binary.LittleEndian.AppendUint64(input, uint64(len(text)))
	return append(input, sha[:]...)
}

// etagScratch is what computing an ETag reuses from one page to the next.
type etagScratch struct {
	gather gathered
	input  []byte
}

var etagScratches = sync.Pool{New: func() any { return new(etagScratch) }}

// release returns the scratch to the pool without the page bytes it pointed to.
func (s *etagScratch) release() {
	clear(s.gather.pieces[:cap(s.gather.pieces)])
	s.gather.pieces = s.gather.pieces[:0]
	etagScratches.Put(s)
}

// gathered joins body bytes gathered piece by piece only when there's more than one piece, into a
// buffer it reuses: what take returns is good until the next take.
type gathered struct {
	pieces [][]byte
	joined []byte
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
		g.joined = g.joined[:0]
		for _, piece := range g.pieces {
			g.joined = append(g.joined, piece...)
		}
		joined = g.joined
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
