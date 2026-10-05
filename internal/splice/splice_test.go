package splice

import (
	"bytes"
	"compress/gzip"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func text(seed uint64, n int) []byte {
	random := rand.New(rand.NewPCG(seed, 1))
	words := []string{"<div class=\"message\">", "campfire", " ", "</div>", "\n", "data-controller", "é", "="}
	var b bytes.Buffer
	for b.Len() < n {
		b.WriteString(words[random.IntN(len(words))])
	}
	return b.Bytes()[:n]
}
func gunzip(t *testing.T, member []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(member))
	if err != nil {
		t.Fatal(err)
	}
	reader.Multistream(false)
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}
func TestCombinedChecksums(t *testing.T) {
	for _, sizes := range [][2]int{{0, 0}, {0, 1}, {1, 0}, {1, 1}, {13, 466000}, {70000, 3}, {1 << 16, 1 << 16}} {
		a, b := text(1, sizes[0]), text(2, sizes[1])
		if got, want := multiply(shift(len(b)), crc32.ChecksumIEEE(a))^crc32.ChecksumIEEE(b), crc32.ChecksumIEEE(append(a, b...)); got != want {
			t.Errorf("%v: %08x want %08x", sizes, got, want)
		}
	}
}
func TestTextAloneMatchesGzip(t *testing.T) {
	modified := time.Unix(1700000000, 0)
	for _, chunks := range [][][]byte{nil, {text(1, 5)}, {text(2, 100000), nil, text(3, 70000)}} {
		var want, got bytes.Buffer
		reference, _ := gzip.NewWriterLevel(&want, level)
		reference.Header.OS, reference.Header.ModTime = 3, modified
		writer := NewWriter(&got, modified)
		for _, chunk := range chunks {
			reference.Write(chunk)
			writer.Write(chunk)
		}
		reference.Close()
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Errorf("%d chunks: member differs from compress/gzip", len(chunks))
		}
	}
}
func TestSplicedPiecesDecodeToTheirText(t *testing.T) {
	list, shell, stamp := Deflate(text(4, 300000)), Deflate(text(5, 9000)), Stored([]byte("1759680000000"))
	long := Stored(text(6, 140000))
	fresh := func(seed uint64, n int) Piece { return Piece{Plain: text(seed, n)} }
	cases := map[string][]Piece{
		"empty":            nil,
		"empty pieces":     {Deflate(nil), Stored(nil), {}},
		"cached":           {shell, stamp, shell, list, shell},
		"cached repeated":  {list, list},
		"fresh then piece": {fresh(7, 5000), list, fresh(8, 5000)},
		"fresh between":    {shell, fresh(9, 40000), fresh(10, 10), stamp, fresh(11, 40000), list},
		"stored blocks":    {long, shell, long},
	}
	for name, pieces := range cases {
		t.Run(name, func(t *testing.T) {
			var member, want bytes.Buffer
			writer := NewWriter(&member, time.Time{})
			for i, piece := range pieces {
				want.Write(piece.Plain)
				if err := writer.WritePiece(piece); err != nil {
					t.Fatal(err)
				}
				if i == 1 {
					writer.Flush()
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if got := gunzip(t, member.Bytes()); !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("decoded %d bytes, want %d", len(got), want.Len())
			}
		})
	}
	// A cached piece costs its blocks, not its text.
	if len(list.Blocks) > len(list.Plain)/4 || !strings.HasPrefix(string(long.Blocks[5:]), string(long.Plain[:100])) {
		t.Fatal(len(list.Blocks), len(list.Plain))
	}
}
func BenchmarkRoomPage(b *testing.B) {
	plain := text(4, 466000)
	list, shell, stamp := Deflate(plain), Deflate(text(5, 9000)), Stored([]byte("1759680000000"))
	b.Run("spliced", func(b *testing.B) {
		b.SetBytes(int64(len(plain)))
		for b.Loop() {
			writer := NewWriter(io.Discard, time.Time{})
			for _, piece := range []Piece{shell, stamp, shell, list, shell} {
				writer.WritePiece(piece)
			}
			writer.Close()
		}
	})
	b.Run("compressed", func(b *testing.B) {
		b.SetBytes(int64(len(plain)))
		for b.Loop() {
			writer := NewWriter(io.Discard, time.Time{})
			for _, piece := range []Piece{shell, stamp, shell, list, shell} {
				writer.Write(piece.Plain)
			}
			writer.Close()
		}
	})
}
