package views

import (
	"strings"
	"testing"

	qt "github.com/valyala/quicktemplate"
)

// A block wrapped where it was written is what its helper returns for the same block, nested
// blocks and the text around them included.
func TestEndCaptureHelpersMatchHelpers(t *testing.T) {
	block := func(qw *qt.Writer) { qw.N().S(`<span class="x">Room &amp; more</span>`) }
	content := HTML(`<span class="x">Room &amp; more</span>`)
	attrs := func() *Attrs { return NewAttrs().ID("room_1").Data("sorted_list_name", "Room").Class("room unread") }
	long := func() *Attrs { return attrs().Title(strings.Repeat("long title ", 80)) } // an opening tag past blockWrap's 512-byte stack buffer
	for _, test := range []struct {
		name   string
		end    func(qw *qt.Writer)
		helper func() HTML
	}{
		{"link_to", func(qw *qt.Writer) { EndCaptureLinkTo(qw, "/rooms/1", attrs()) }, func() HTML { return LinkToBlock(content, "/rooms/1", attrs()) }},
		{"link_to long", func(qw *qt.Writer) { EndCaptureLinkTo(qw, "/rooms/1", long()) }, func() HTML { return LinkToBlock(content, "/rooms/1", long()) }},
		{"link_to_room", func(qw *qt.Writer) { EndCaptureLinkToRoom(qw, 1, attrs()) }, func() HTML { return LinkToRoom(content, 1, attrs()) }},
		{"button_to", func(qw *qt.Writer) { EndCaptureButtonTo(qw, "/rooms/1", attrs().Method("delete")) }, func() HTML { return ButtonToBlock(content, "/rooms/1", attrs().Method("delete")) }},
		{"sidebar_turbo_frame_tag", EndCaptureSidebarTurboFrameTag, func() HTML { return SidebarTurboFrameTagBlock(content) }},
	} {
		got := RenderString(0, func(qw *qt.Writer) {
			qw.N().S("before ")
			BeginCapture(qw)
			BeginCapture(qw)
			block(qw)
			test.end(qw)
			inner := EndCapture(qw)
			qw.N().S(string(inner))
			qw.N().S(" after")
		})
		if want := "before " + string(test.helper()) + " after"; got != want {
			t.Errorf("%s:\n got %s\nwant %s", test.name, got, want)
		}
	}
}

// The tag helpers templates write into the page write what they return, into a page or any writer.
func TestStreamedTagsMatchHelpers(t *testing.T) {
	ctx := &ViewContext{}
	stream := func(qw *qt.Writer) {
		qw.N().S("<p>")
		StreamImageTag(qw, ctx, "https://example.com/a.png", NewAttrs().Size("20x30").Class("x & y"))
		StreamBuilderTag(qw, "turbo_frame", NewAttrs().ID("f"))
		StreamBuilderTag(qw, "meta", NewAttrs().Name("n").Attr("content", "c"))
		StreamTurboStreamFrom(qw, "signed--name")
		qw.N().S("</p>")
	}
	want := "<p>" + string(ImageTag(ctx, "https://example.com/a.png", NewAttrs().Size("20x30").Class("x & y"))) +
		string(BuilderTag("turbo_frame", NewAttrs().ID("f"))) + string(BuilderTag("meta", NewAttrs().Name("n").Attr("content", "c"))) +
		string(TurboStreamFrom("signed--name")) + "</p>"
	if got := RenderString(0, stream); got != want {
		t.Errorf("into a page:\n got %s\nwant %s", got, want)
	}
	var out strings.Builder
	qw := qt.AcquireWriter(&out)
	stream(qw)
	qt.ReleaseWriter(qw)
	if out.String() != want {
		t.Errorf("into a writer:\n got %s\nwant %s", out.String(), want)
	}
}

// withDefaultData's one hash is the reference's merge of the attributes before the first data
// attribute, the defaults with the caller's data attributes, and the rest.
func TestWithDefaultDataOrder(t *testing.T) {
	defaults := []attrEntry{{"data-a", textAttr("1")}, {"data-b", textAttr("2")}}
	for _, test := range []struct {
		attrs *Attrs
		want  string
	}{
		{nil, ` data-a="1" data-b="2"`},
		{NewAttrs().ID("x").Class("c"), ` id="x" class="c" data-a="1" data-b="2"`},
		{NewAttrs().ID("x").Data("b", "9").Class("c").Data("z", "3"), ` id="x" data-a="1" data-b="9" data-z="3" class="c"`},
		{NewAttrs().Data("a", "0").ID("x").Style("s").Class("c").Title("t"), ` data-a="0" data-b="2" id="x" style="s" class="c" title="t"`},
	} {
		var b tagBuilder
		test.attrs.withDefaultData(defaults).renderInto(&b)
		if b.String() != test.want {
			t.Errorf("got %s\nwant %s", b.String(), test.want)
		}
	}
}
