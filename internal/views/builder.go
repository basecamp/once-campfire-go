package views

import (
	"slices"
	"unicode/utf8"
	"unsafe"

	qt "github.com/valyala/quicktemplate"
)

// tagBuilder is what the helpers build tags in: strings.Builder's methods over a byte slice, which
// can be the page's own text, so that a tag the template writes is built where it goes.
type tagBuilder struct{ buf []byte }

func (b *tagBuilder) WriteString(s string) (int, error) {
	b.buf = append(b.buf, s...)
	return len(s), nil
}

func (b *tagBuilder) WriteByte(c byte) error {
	b.buf = append(b.buf, c)
	return nil
}

func (b *tagBuilder) WriteRune(r rune) (int, error) {
	n := len(b.buf)
	b.buf = utf8.AppendRune(b.buf, r)
	return len(b.buf) - n, nil
}

func (b *tagBuilder) Grow(n int) { b.buf = slices.Grow(b.buf, n) }
func (b *tagBuilder) Len() int   { return len(b.buf) }

// String is what was built. It shares the bytes, as strings.Builder's does: only call it on a
// builder of its own, not one building into the page.
func (b *tagBuilder) String() string { return unsafe.String(unsafe.SliceData(b.buf), len(b.buf)) }

// intoPage is a builder that writes into the page's text, for a tag helper whose tag goes where
// the template writes it ({%= ImageTag(...) %} calls StreamImageTag); writeOut finishes it. A
// template rendered into another writer gets the tag written to that writer instead.
func intoPage(qw *qt.Writer) (tagBuilder, *Writer) {
	if w, ok := qw.W().(*Writer); ok {
		return tagBuilder{w.page.Text}, w
	}
	return tagBuilder{}, nil
}

func (b *tagBuilder) writeOut(qw *qt.Writer, w *Writer) {
	if w != nil {
		w.page.Text = b.buf
		return
	}
	qw.N().SZ(b.buf)
}
