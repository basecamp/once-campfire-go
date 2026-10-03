// Package qrcode preserves rqrcode_core 2.1.0's segment, capacity and mask
// choices and rqrcode 3.2.0's SVG output. See the pinned Rust qr_code/rqrcode.rs.
package qrcode

import (
	"fmt"
	"math"
	"math/bits"
	"strings"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:"

type segment struct {
	data []byte
	mode int
}

func newSegment(data []byte) segment {
	mode := 1
	for _, b := range data {
		if b < '0' || b > '9' {
			mode = 2
			break
		}
	}
	if mode == 2 {
		for _, b := range data {
			if !strings.ContainsRune(alphabet, rune(b)) {
				mode = 4
				break
			}
		}
	}
	return segment{data, mode}
}
func (s segment) lengthBits(version int) int {
	group := 0
	if version >= 10 {
		group = 1
	}
	if version >= 27 {
		group = 2
	}
	switch s.mode {
	case 1:
		return [3]int{10, 12, 14}[group]
	case 2:
		return [3]int{9, 11, 13}[group]
	default:
		return [3]int{8, 16, 16}[group]
	}
}
func (s segment) size(version int) int {
	n := len(s.data)
	content := n * 8
	switch s.mode {
	case 1:
		content = n/3*10 + [3]int{0, 4, 7}[n%3]
	case 2:
		content = n/2*11 + n%2*6
	}
	return 4 + s.lengthBits(version) + content
}

type bitBuffer struct {
	data []byte
	n    int
}

func (b *bitBuffer) put(value, n int) {
	for i := n - 1; i >= 0; i-- {
		index := b.n / 8
		if len(b.data) <= index {
			b.data = append(b.data, 0)
		}
		if value>>i&1 != 0 {
			b.data[index] |= 0x80 >> uint(b.n%8)
		}
		b.n++
	}
}
func (s segment) write(b *bitBuffer, version int) {
	b.put(s.mode, 4)
	b.put(len(s.data), s.lengthBits(version))
	switch s.mode {
	case 1:
		for i := 0; i < len(s.data); i += 3 {
			part := s.data[i:min(i+3, len(s.data))]
			value := 0
			for _, c := range part {
				value = value*10 + int(c-'0')
			}
			b.put(value, [4]int{0, 4, 7, 10}[len(part)])
		}
	case 2:
		for i := 0; i < len(s.data); i += 2 {
			value := strings.IndexByte(alphabet, s.data[i])
			n := 6
			if i+1 < len(s.data) {
				value = value*45 + strings.IndexByte(alphabet, s.data[i+1])
				n = 11
			}
			b.put(value, n)
		}
	default:
		for _, c := range s.data {
			b.put(int(c), 8)
		}
	}
}

var gfExp, gfLog = func() ([256]int, [256]int) {
	var exp, log [256]int
	for i := 0; i < 8; i++ {
		exp[i] = 1 << i
	}
	for i := 8; i < 256; i++ {
		exp[i] = exp[i-4] ^ exp[i-5] ^ exp[i-6] ^ exp[i-8]
	}
	for i := 0; i < 255; i++ {
		log[exp[i]] = i
	}
	return exp, log
}()

func gexp(n int) int {
	for n < 0 {
		n += 255
	}
	for n >= 256 {
		n -= 255
	}
	return gfExp[n]
}
func polynomial(values []int, shift int) []int {
	offset := 0
	for offset < len(values) && values[offset] == 0 {
		offset++
	}
	out := make([]int, len(values)-offset+shift)
	copy(out, values[offset:])
	return out
}
func multiply(a, b []int) []int {
	out := make([]int, len(a)+len(b)-1)
	for i, x := range a {
		for j, y := range b {
			out[i+j] ^= gexp(gfLog[x] + gfLog[y])
		}
	}
	return polynomial(out, 0)
}
func modulo(a, b []int) []int {
	for len(a) >= len(b) {
		ratio := gfLog[a[0]] - gfLog[b[0]]
		next := append([]int(nil), a...)
		for i, x := range b {
			next[i] ^= gexp(gfLog[x] + ratio)
		}
		a = polynomial(next, 0)
	}
	return a
}
func codewords(s segment, version int) []byte {
	type block struct{ total, data int }
	var groups []block
	capacity := 0
	table := blocks[version-1]
	for i := 0; i < len(table); i += 3 {
		for range table[i] {
			groups = append(groups, block{table[i+1], table[i+2]})
			capacity += table[i+2] * 8
		}
	}
	b := bitBuffer{}
	s.write(&b, version)
	if b.n+4 <= capacity {
		b.put(0, 4)
	}
	for b.n%8 != 0 {
		b.put(0, 1)
	}
	for b.n < capacity {
		b.put(0xec, 8)
		if b.n < capacity {
			b.put(0x11, 8)
		}
	}
	var dc, ec [][]int
	offset := 0
	for _, group := range groups {
		data := make([]int, group.data)
		for i := range data {
			data[i] = int(b.data[offset+i])
		}
		offset += group.data
		count := group.total - group.data
		poly := []int{1}
		for i := 0; i < count; i++ {
			poly = multiply(poly, []int{1, gexp(i)})
		}
		remainder := modulo(polynomial(data, count), poly)
		errorWords := make([]int, count)
		copy(errorWords[count-len(remainder):], remainder)
		dc = append(dc, data)
		ec = append(ec, errorWords)
	}
	var result []byte
	for _, groups := range [][][]int{dc, ec} {
		longest := 0
		for _, group := range groups {
			longest = max(longest, len(group))
		}
		for i := 0; i < longest; i++ {
			for _, group := range groups {
				if i < len(group) {
					result = append(result, byte(group[i]))
				}
			}
		}
	}
	return result
}
func mask(pattern, row, col int) bool {
	switch pattern {
	case 0:
		return (row+col)%2 == 0
	case 1:
		return row%2 == 0
	case 2:
		return col%3 == 0
	case 3:
		return (row+col)%3 == 0
	case 4:
		return (row/2+col/3)%2 == 0
	case 5:
		return row*col%2+row*col%3 == 0
	case 6:
		return (row*col%2+row*col%3)%2 == 0
	default:
		return (row*col%3+(row+col)%2)%2 == 0
	}
}
func bch(data, shift, generator int) int {
	d := data << shift
	for bits.Len(uint(d)) >= bits.Len(uint(generator)) {
		d ^= generator << (bits.Len(uint(d)) - bits.Len(uint(generator)))
	}
	return data<<shift | d
}

type grid [][]int8

func newGrid(n int) grid {
	g := make(grid, n)
	for i := range g {
		g[i] = make([]int8, n)
		for j := range g[i] {
			g[i][j] = -1
		}
	}
	return g
}
func (g grid) set(y, x int, dark bool) {
	if dark {
		g[y][x] = 1
	} else {
		g[y][x] = 0
	}
}
func (g grid) probe(row, col int) {
	n := len(g)
	for r := -1; r <= 7; r++ {
		for c := -1; c <= 7; c++ {
			y, x := row+r, col+c
			if y < 0 || y >= n || x < 0 || x >= n {
				continue
			}
			g.set(y, x, r >= 0 && r <= 6 && (c == 0 || c == 6) || c >= 0 && c <= 6 && (r == 0 || r == 6) || r >= 2 && r <= 4 && c >= 2 && c <= 4)
		}
	}
}
func (g grid) format(pattern int, test bool) {
	n := len(g)
	value := bch(2<<3|pattern, 10, 0x537) ^ 0x5412
	for i := 0; i < 15; i++ {
		dark := !test && value>>i&1 != 0
		row := n - 15 + i
		if i < 6 {
			row = i
		} else if i < 8 {
			row = i + 1
		}
		g.set(row, 8, dark)
		col := 15 - i - 1
		if i < 8 {
			col = n - i - 1
		} else if i < 9 {
			col = 15 - i
		}
		g.set(8, col, dark)
	}
	g.set(n-8, 8, !test)
}
func (g grid) version(version int, test bool) {
	n := len(g)
	value := bch(version, 12, 0x1f25)
	for i := 0; i < 18; i++ {
		dark := !test && value>>i&1 != 0
		g.set(i/3, i%3+n-11, dark)
		g.set(i%3+n-11, i/3, dark)
	}
}
func (g grid) mapData(data []byte, pattern int) {
	n := len(g)
	inc, row, bit, index := -1, n-1, 7, 0
	for col := n - 1; col >= 1; col -= 2 {
		c0 := col
		if col <= 6 {
			c0--
		}
		for {
			for c := 0; c < 2; c++ {
				x := c0 - c
				if g[row][x] < 0 {
					dark := index < len(data) && data[index]>>bit&1 != 0
					if mask(pattern, row, x) {
						dark = !dark
					}
					g.set(row, x, dark)
					bit--
					if bit < 0 {
						index++
						bit = 7
					}
				}
			}
			row += inc
			if row < 0 || row >= n {
				row -= inc
				inc = -inc
				break
			}
		}
	}
}
func lostPoints(g grid) float64 {
	n := len(g)
	points, dark := 0, 0
	for row := 0; row < n; row++ {
		for col := 0; col < n; col++ {
			value := g[row][col]
			if value == 1 {
				dark++
			}
			same := 0
			for y := max(0, row-1); y <= min(n-1, row+1); y++ {
				for x := max(0, col-1); x <= min(n-1, col+1); x++ {
					if (y != row || x != col) && g[y][x] == value {
						same++
					}
				}
			}
			if same > 5 {
				points += 3 + same - 5
			}
			if row+1 < n && col+1 < n && g[row+1][col] == value && g[row][col+1] == value && g[row+1][col+1] == value {
				points += 3
			}
		}
	}
	finder := func(a, b, c, d, e, f, h int8) bool {
		return a == 1 && b == 0 && c == 1 && d == 1 && e == 1 && f == 0 && h == 1
	}
	for start := 0; start < n-6; start++ {
		for line := 0; line < n; line++ {
			r := g[line]
			if finder(r[start], r[start+1], r[start+2], r[start+3], r[start+4], r[start+5], r[start+6]) {
				points += 40
			}
			if finder(g[start][line], g[start+1][line], g[start+2][line], g[start+3][line], g[start+4][line], g[start+5][line], g[start+6][line]) {
				points += 40
			}
		}
	}
	return float64(points) + math.Abs(100*float64(dark)/float64(n*n)-50)/5*10
}
func Modules(data []byte) ([][]bool, int) {
	s := newSegment(data)
	version := 0
	for i, capacity := range capacities {
		if s.size(i+1) < capacity {
			version = i + 1
			break
		}
	}
	if version == 0 {
		return nil, 0
	}
	n := version*4 + 17
	common := newGrid(n)
	common.probe(0, 0)
	common.probe(n-7, 0)
	common.probe(0, n-7)
	for _, row := range positions[version-1] {
		for _, col := range positions[version-1] {
			if common[row][col] >= 0 {
				continue
			}
			for r := -2; r <= 2; r++ {
				for c := -2; c <= 2; c++ {
					common.set(row+r, col+c, r == -2 || r == 2 || c == -2 || c == 2 || r == 0 && c == 0)
				}
			}
		}
	}
	for i := 8; i < n-8; i++ {
		common.set(i, 6, i%2 == 0)
		common.set(6, i, i%2 == 0)
	}
	words := codewords(s, version)
	makeGrid := func(pattern int, test bool) grid {
		g := newGrid(n)
		for i := range g {
			copy(g[i], common[i])
		}
		g.format(pattern, test)
		if version >= 7 {
			g.version(version, test)
		}
		g.mapData(words, pattern)
		return g
	}
	best, score := 0, math.MaxFloat64
	for pattern := 0; pattern < 8; pattern++ {
		points := lostPoints(makeGrid(pattern, true))
		if points < score {
			best, score = pattern, points
		}
	}
	g := makeGrid(best, false)
	out := make([][]bool, n)
	for i := range out {
		out[i] = make([]bool, n)
		for j := range out[i] {
			out[i][j] = g[i][j] == 1
		}
	}
	return out, version
}
func SVG(data []byte) (string, bool) {
	modules, _ := Modules(data)
	if modules == nil {
		return "", false
	}
	dimension := len(modules) * 11
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" standalone="yes"?>`)
	fmt.Fprintf(&out, `<svg version="1.1" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:ev="http://www.w3.org/2001/xml-events" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" x="0" y="0" fill="white"/>`, dimension, dimension, dimension, dimension)
	for row, cells := range modules {
		for col, dark := range cells {
			if dark {
				fmt.Fprintf(&out, `<rect width="11" height="11" x="%d" y="%d" fill="black"/>`, col*11, row*11)
			}
		}
	}
	out.WriteString("</svg>")
	return out.String(), true
}
