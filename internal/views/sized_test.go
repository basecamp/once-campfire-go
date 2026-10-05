package views

import (
	"strings"
	"testing"

	qt "github.com/valyala/quicktemplate"
)

func renderParagraph(size *RenderSize, body string) *RecordedPage {
	return size.Render(0, func(qw *qt.Writer) {
		qw.N().S("<p>")
		qw.N().S(body)
		qw.N().S("</p>")
	})
}

func TestReservesTheLastLengthWithHeadroomUpToACap(t *testing.T) {
	var size RenderSize
	body := strings.Repeat("x", 8000)
	renderParagraph(&size, body)
	if got := size.capacity(0); got != 8007+8007/8 {
		t.Errorf("got %d", got)
	}
	// A shorter page sizes the next render: one long page doesn't stay reserved.
	renderParagraph(&size, body[:7000])
	if got := size.capacity(0); got != 7007+7007/8 {
		t.Errorf("got %d", got)
	}
	renderParagraph(&size, strings.Repeat("x", 2*maxReserve))
	if got := size.capacity(0); got != maxReserve {
		t.Errorf("got %d", got)
	}
}

func TestEachCallSiteHasItsOwnSize(t *testing.T) {
	var short, long RenderSize
	renderParagraph(&long, strings.Repeat("x", 4000))
	if page := renderParagraph(&short, "short"); cap(page.Text) >= 100 {
		t.Errorf("reserved %d", cap(page.Text))
	}
}
