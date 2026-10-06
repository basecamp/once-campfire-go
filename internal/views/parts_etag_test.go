package views

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"strings"
	"testing"
	"unsafe"
)

// The ETag hashes the same records, in the same order, as the straightforward version below (runs
// gathered first, each record written to the hash in turn), for pages of every shape: no
// fragments, small ones only, large ones back to back, and gaps on either side of maxGlue.
func TestETagMatchesPreviousImplementation(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	fragments := []*Fragment{}
	for _, size := range []int{1, 100, 1023, 1024, 2000, 5000} {
		fragments = append(fragments, NewFragment(strings.Repeat(string(rune('a'+size%26)), size)))
	}
	text := func() string {
		sizes := []int{0, 0, 1, 50, 256, 257, 1000, 3000}
		return strings.Repeat("t", sizes[random.IntN(len(sizes))])
	}
	if got := (&RecordedPage{}).ETag(); got != "" {
		t.Fatalf("empty page: %q", got)
	}
	for range 3000 {
		page := &RecordedPage{}
		for range random.IntN(8) {
			page.Text = append(page.Text, text()...)
			page.Fragments = append(page.Fragments, Placed{Offset: len(page.Text), Fragment: fragments[random.IntN(len(fragments))]})
		}
		page.Text = append(page.Text, text()...)
		if got, want := page.ETag(), etagUnbuffered(page); got != want {
			t.Fatalf("page %d text, %d fragments: %s, want %s", len(page.Text), len(page.Fragments), got, want)
		}
	}
}

func etagUnbuffered(p *RecordedPage) string {
	gather := gatheredUnbuffered{}
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
			gather.push(unsafe.Slice(unsafe.StringData(placed.Fragment.HTML), len(placed.Fragment.HTML)))
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

// gatheredUnbuffered joins body bytes gathered piece by piece only when there's more than one piece.
type gatheredUnbuffered struct {
	pieces [][]byte
}

func (g *gatheredUnbuffered) push(b []byte) {
	if len(b) > 0 {
		g.pieces = append(g.pieces, b)
	}
}

func (g *gatheredUnbuffered) take() []byte {
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
