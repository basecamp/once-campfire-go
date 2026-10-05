package views

import (
	"sync/atomic"

	qt "github.com/valyala/quicktemplate"
)

// Page renders into a buffer sized from the last render at the same call site (the reference's
// sized.rs): a guess from the template's literal text would leave a room page's buffer growing
// several times. Only the page's text goes into the buffer: its cached fragments are recorded
// instead.

// maxReserve is the most a render reserves up front. Only the last render's length is kept, so one
// huge page sizes just the next render; this cap bounds even that one.
const maxReserve = 1 << 20

// RenderSize is the length of the last page rendered at one call site: declare one per call site,
// `var showSize views.RenderSize`, and render with showSize.Render(...).
type RenderSize struct {
	last atomic.Int64
}

// Render is Render into a text buffer sized from the last render here.
func (s *RenderSize) Render(sizeHint int, render func(qw *qt.Writer)) *RecordedPage {
	page := Render(s.capacity(sizeHint), render)
	s.last.Store(int64(len(page.Text)))
	return page
}

// capacity is the last length plus an eighth, so a page a little longer than the last still fits.
func (s *RenderSize) capacity(sizeHint int) int {
	last := int(s.last.Load())
	return max(min(last+last/8, maxReserve), sizeHint)
}
