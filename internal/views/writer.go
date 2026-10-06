package views

import (
	"crypto/sha256"
	"io"
	"math/bits"
	"sync"
	"sync/atomic"
	"unsafe"

	qt "github.com/valyala/quicktemplate"
)

// Fragment is a rendered fragment as the fragment cache keeps it, shared by every page that shows
// it (the reference's Arc<String>). Its SHA-256 is remembered with it, as the reference's kit
// remembers a fragment's digest with its Arc.
type Fragment struct {
	HTML   string
	digest atomic.Pointer[[32]byte]
}

func NewFragment(html string) *Fragment { return &Fragment{HTML: html} }

// Digest is the fragment's SHA-256, computed once.
func (f *Fragment) Digest() [32]byte {
	if d := f.digest.Load(); d != nil {
		return *d
	}
	d := sha256.Sum256(unsafe.Slice(unsafe.StringData(f.HTML), len(f.HTML)))
	f.digest.Store(&d)
	return d
}

// Shorter fragments are copied into the text around them (the reference's MIN_RECORDED): the
// page parts keep fragments under 1 KB in the text anyway, and recording every short write would
// cost more than copying it.
const minRecorded = 1024

// Placed is a cached fragment at its byte offset in a page's text.
type Placed struct {
	Offset   int
	Fragment *Fragment
}

// RecordedPage is a rendered page: its text, and each cached fragment handed to it with the fragment's
// offset in the text, in order (the reference's RecordedPage). Writing the text with the
// fragments spliced in at their offsets gives the page's bytes.
type RecordedPage struct {
	Text      []byte
	Fragments []Placed
}

// Len is the length of the page's bytes.
func (p *RecordedPage) Len() int {
	n := len(p.Text)
	for _, placed := range p.Fragments {
		n += len(placed.Fragment.HTML)
	}
	return n
}

// AppendTo appends the page's bytes to b.
func (p *RecordedPage) AppendTo(b []byte) []byte {
	position := 0
	for _, placed := range p.Fragments {
		b = append(b, p.Text[position:placed.Offset]...)
		b = append(b, placed.Fragment.HTML...)
		position = placed.Offset
	}
	return append(b, p.Text[position:]...)
}

// WriteTo writes the page's bytes to w, a text segment or fragment at a time.
func (p *RecordedPage) WriteTo(w io.Writer) (int64, error) {
	var total int64
	write := func(b []byte) error {
		n, err := w.Write(b)
		total += int64(n)
		return err
	}
	position := 0
	for _, placed := range p.Fragments {
		if err := write(p.Text[position:placed.Offset]); err != nil {
			return total, err
		}
		if err := write(unsafe.Slice(unsafe.StringData(placed.Fragment.HTML), len(placed.Fragment.HTML))); err != nil {
			return total, err
		}
		position = placed.Offset
	}
	return total, write(p.Text[position:])
}

// String is the page's bytes as a string.
func (p *RecordedPage) String() string { return string(p.AppendTo(make([]byte, 0, p.Len()))) }

// Writer is what templates render into: a page being recorded, or (with recording off) plain
// text. A capture (a filter block's content) is written in place; captures holds where each open
// one starts in the page's text.
type Writer struct {
	page      *RecordedPage
	recording bool
	captures  []int
}

func (w *Writer) Write(p []byte) (int, error) {
	w.page.Text = append(w.page.Text, p...)
	return len(p), nil
}

func (w *Writer) WriteString(s string) (int, error) {
	w.page.Text = append(w.page.Text, s...)
	return len(s), nil
}

var writers = sync.Pool{New: func() any { return new(Writer) }}

// Pages whose text buffers are at least 1 << minPooledClass bytes go back to a pool once their
// response is written (ReleasePage), one pool per power-of-two capacity: a room page's text is
// rendered thousands of times a second, and a fresh buffer each time is most of what the
// garbage collector has to keep up with.
const (
	minPooledClass = 12
	maxPooledClass = 21
)

var pagePools [maxPooledClass + 1]sync.Pool

// pooledPage is a page from the pool whose text holds at least sizeHint bytes, or nil.
func pooledPage(sizeHint int) *RecordedPage {
	class := bits.Len(uint(sizeHint - 1))
	if sizeHint < 1<<minPooledClass || class > maxPooledClass {
		return nil
	}
	if page, ok := pagePools[class].Get().(*RecordedPage); ok {
		return page
	}
	return &RecordedPage{Text: make([]byte, 0, 1<<class)}
}

// ReleasePage hands a page's buffers back for later renders. The page must not be used again: call
// it once the page's bytes have been written.
func ReleasePage(page *RecordedPage) {
	class := bits.Len(uint(cap(page.Text))) - 1
	if class < minPooledClass || class > maxPooledClass {
		return
	}
	clear(page.Fragments)
	page.Text, page.Fragments = page.Text[:0], page.Fragments[:0]
	pagePools[class].Put(page)
}

// Render renders a page with its cached fragments recorded rather than copied, into a text
// buffer of sizeHint bytes.
func Render(sizeHint int, render func(qw *qt.Writer)) *RecordedPage {
	return renderPage(sizeHint, true, render)
}

// RenderString renders without recording: the whole output as one string.
func RenderString(sizeHint int, render func(qw *qt.Writer)) string {
	page := renderPage(sizeHint, false, render)
	return unsafe.String(unsafe.SliceData(page.Text), len(page.Text))
}

func renderPage(sizeHint int, recording bool, render func(qw *qt.Writer)) *RecordedPage {
	w := writers.Get().(*Writer)
	if w.page = pooledPage(sizeHint); w.page == nil {
		w.page = &RecordedPage{Text: make([]byte, 0, sizeHint)}
	}
	w.recording = recording
	qw := qt.AcquireWriter(w)
	render(qw)
	qt.ReleaseWriter(qw)
	page := w.page
	w.page = nil
	w.captures = w.captures[:0]
	writers.Put(w)
	return page
}

func writerOf(qw *qt.Writer) *Writer {
	w, ok := qw.W().(*Writer)
	if !ok {
		panic("views: template not rendering into a views.Writer")
	}
	return w
}

// StreamFragment writes a cached fragment: into the page being recorded at its offset when it's
// large enough, else as text. Templates write fragments with `{%= Fragment(f) %}`.
func StreamFragment(qw *qt.Writer, f *Fragment) {
	w := writerOf(qw)
	if w.recording && len(w.captures) == 0 && len(f.HTML) >= minRecorded {
		w.page.Fragments = append(w.page.Fragments, Placed{Offset: len(w.page.Text), Fragment: f})
		return
	}
	w.WriteString(f.HTML)
}

// BeginCapture starts capturing what the template writes, for a filter block (`link_to ... do`).
func BeginCapture(qw *qt.Writer) {
	w := writerOf(qw)
	w.captures = append(w.captures, len(w.page.Text))
}

// EndCapture ends the innermost capture and takes its content out of the page, for a block helper
// to wrap.
func EndCapture(qw *qt.Writer) HTML {
	w := writerOf(qw)
	start := w.endCapture()
	content := HTML(w.page.Text[start:])
	w.page.Text = w.page.Text[:start]
	return content
}

// blockWrap wraps the block just captured where it was written, for a block helper whose output is
// an opening tag, the block and a closing tag (EndCaptureLinkTo and the others in
// helpers_filters.go): the helper writes its opening tag into tag, after the block, and close moves
// it in front of the block and closes it. The block isn't copied out and written back.
type blockWrap struct {
	w          *Writer
	start, end int // the block, in the page's text
	tag        tagBuilder
}

// wrapBlock ends the innermost capture, to wrap it.
func wrapBlock(qw *qt.Writer) blockWrap {
	w := writerOf(qw)
	start := w.endCapture()
	return blockWrap{w: w, start: start, end: len(w.page.Text), tag: tagBuilder{w.page.Text}}
}

func (b *blockWrap) close(closing string) {
	text := b.tag.buf
	// The opening tag is set aside (on the stack when it fits) while the block moves after it.
	var small [512]byte
	open := append(small[:0], text[b.end:]...)
	copy(text[b.start+len(open):], text[b.start:b.end])
	copy(text[b.start:], open)
	b.w.page.Text = append(text, closing...)
}

func (w *Writer) endCapture() int {
	n := len(w.captures) - 1
	start := w.captures[n]
	w.captures = w.captures[:n]
	return start
}
