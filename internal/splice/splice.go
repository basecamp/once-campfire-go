// Package splice builds one gzip member from text deflated apart. A cached fragment is
// compressed once, when it enters the cache, and its blocks are copied into each response
// containing it, as Rust's deflater::splice does.
package splice

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"io"
	"sync"
	"time"
)

// Rack::Deflater's level.
const level = 6

var flatePool = sync.Pool{New: func() any { writer, _ := flate.NewWriter(nil, level); return writer }}

// A Piece is body text and, when Deflate or Stored made it, its deflate blocks: none final
// and ending on a byte boundary, so they can follow any other piece's. A Piece of Plain
// alone is compressed by the Writer.
type Piece struct {
	Plain, Blocks []byte
	crc, shift    uint32
}

// Deflate compresses text that outlives the response, without reference to what precedes it.
func Deflate(plain []byte) Piece {
	var blocks bytes.Buffer
	writer := flatePool.Get().(*flate.Writer)
	writer.Reset(&blocks)
	writer.Write(plain)
	writer.Flush()
	writer.Reset(nil)
	flatePool.Put(writer)
	return piece(plain, blocks.Bytes())
}

// Stored frames text without compressing it: for a few bytes that change between responses
// and sit between cached pieces, where a compressor would have to start over.
func Stored(plain []byte) Piece {
	blocks := make([]byte, 0, len(plain)+5*(len(plain)/0xffff+1))
	for rest := plain; len(rest) > 0; {
		n := min(len(rest), 0xffff)
		blocks = append(blocks, 0, byte(n), byte(n>>8), ^byte(n), ^byte(n>>8))
		blocks = append(blocks, rest[:n]...)
		rest = rest[n:]
	}
	return piece(plain, blocks)
}
func piece(plain, blocks []byte) Piece {
	return Piece{Plain: plain, Blocks: blocks, crc: crc32.ChecksumIEEE(plain), shift: shift(len(plain))}
}

// Writer writes a gzip member. Text alone gives the bytes compress/gzip gives.
type Writer struct {
	dst              io.Writer
	flate            *flate.Writer
	header           [10]byte
	crc, size        uint32
	begun, open, old bool
}

func NewWriter(dst io.Writer, modified time.Time) *Writer {
	w := &Writer{dst: dst, header: [10]byte{0: 0x1f, 1: 0x8b, 2: 8, 9: 3}}
	if modified.After(time.Unix(0, 0)) {
		binary.LittleEndian.PutUint32(w.header[4:8], uint32(modified.Unix()))
	}
	return w
}
func (w *Writer) begin() error {
	if w.begun {
		return nil
	}
	w.begun = true
	_, err := w.dst.Write(w.header[:])
	return err
}
func (w *Writer) Write(plain []byte) (int, error) {
	if err := w.begin(); err != nil || len(plain) == 0 {
		return 0, err
	}
	if w.flate == nil {
		w.flate = flatePool.Get().(*flate.Writer)
		w.flate.Reset(w.dst)
	} else if w.old {
		// Its window holds text from before a spliced piece, at distances that are now wrong.
		w.flate.Reset(w.dst)
	}
	w.open, w.old = true, false
	w.crc = crc32.Update(w.crc, crc32.IEEETable, plain)
	w.size += uint32(len(plain))
	return w.flate.Write(plain)
}

// WritePiece copies the piece's blocks, or compresses a piece that has none.
func (w *Writer) WritePiece(piece Piece) error {
	if piece.Blocks == nil || len(piece.Plain) == 0 {
		_, err := w.Write(piece.Plain)
		return err
	}
	if err := w.begin(); err != nil {
		return err
	}
	if w.open {
		if err := w.flate.Flush(); err != nil {
			return err
		}
		w.open, w.old = false, true
	}
	w.crc = multiply(piece.shift, w.crc) ^ piece.crc
	w.size += uint32(len(piece.Plain))
	_, err := w.dst.Write(piece.Blocks)
	return err
}
func (w *Writer) Flush() error {
	if err := w.begin(); err != nil || !w.open {
		return err
	}
	return w.flate.Flush()
}

// Close ends the member and releases the compressor. It does not close the destination.
func (w *Writer) Close() error {
	err := w.begin()
	trailer := make([]byte, 0, 10)
	if w.open && err == nil {
		err = w.flate.Close()
	} else {
		// What flate's Close writes after a flush: an empty final block.
		trailer = append(trailer, 3, 0)
	}
	if w.flate != nil {
		w.flate.Reset(nil)
		flatePool.Put(w.flate)
		w.flate, w.open = nil, false
	}
	if err != nil {
		return err
	}
	trailer = binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(trailer, w.crc), w.size)
	_, err = w.dst.Write(trailer)
	return err
}

// The CRC-32 of joined text from the CRCs of its halves, as zlib's crc32_combine_op:
// crc(ab) = crc(a)·x^(8·len(b)) + crc(b) in GF(2)[x] modulo the CRC polynomial, bit-reversed.
var powers = func() (table [32]uint32) {
	table[0] = 1 << 30
	for i := 1; i < len(table); i++ {
		table[i] = multiply(table[i-1], table[i-1])
	}
	return
}()

func multiply(a, b uint32) (product uint32) {
	for m := uint32(1) << 31; m != 0; m >>= 1 {
		if a&m != 0 {
			product ^= b
		}
		if b&1 != 0 {
			b = b>>1 ^ crc32.IEEE
		} else {
			b >>= 1
		}
	}
	return
}

// x^(8n).
func shift(n int) uint32 {
	product := uint32(1) << 31
	for k := 3; n != 0; n, k = n>>1, k+1 {
		if n&1 != 0 {
			product = multiply(powers[k&31], product)
		}
	}
	return product
}
