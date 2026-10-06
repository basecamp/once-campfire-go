package views

import "testing"

func renderAttrs(a *Attrs) string {
	var b tagBuilder
	a.renderInto(&b)
	return b.String()
}

func TestRendersAttributesInOrderWithRailsValueRules(t *testing.T) {
	attrs := NewAttrs().
		Class("a&b").
		Hidden().
		Required(false).
		Data("turbo_permanent", true).
		Aria("hidden", "true").
		Tabindex(-1).
		Attr("data-action", HTML(`a->"b"`))
	expected := ` class="a&amp;b" hidden="hidden" data-turbo-permanent="true" aria-hidden="true" tabindex="-1" data-action="a->&quot;b&quot;"`
	if got := renderAttrs(attrs); got != expected {
		t.Errorf("got %s", got)
	}
	if got := renderAttrs(attrs.clone()); got != expected {
		t.Errorf("clone: got %s", got)
	}
}

func TestAssignmentKeepsPosition(t *testing.T) {
	attrs := NewAttrs().Value("x").Class("c")
	attrs.Attr("value", "y")
	if got := renderAttrs(attrs); got != ` value="y" class="c"` {
		t.Errorf("got %s", got)
	}
	view := attrs.clone().Attr("value", "z").Attr("id", "i")
	if got := renderAttrs(view); got != ` value="z" class="c" id="i"` {
		t.Errorf("got %s", got)
	}
}

func TestDefaultDataGoesWhereTheCallersDataStarts(t *testing.T) {
	attrs := NewAttrs().ID("x").Data("b", "caller").Class("c").Data("a", "override")
	defaults := []attrEntry{{"data-a", textAttr("default")}, {"data-z", textAttr("z")}}
	if got := renderAttrs(attrs.withDefaultData(defaults)); got != ` id="x" data-a="override" data-z="z" data-b="caller" class="c"` {
		t.Errorf("got %s", got)
	}
}

func TestTags(t *testing.T) {
	for _, c := range []struct{ got, want HTML }{
		{ContentTag("textarea", NewAttrs().ID("t"), "x"), "<textarea id=\"t\">\nx</textarea>"},
		{ContentTagText("span", NewAttrs(), "<b>"), "<span>&lt;b&gt;</span>"},
		{ContentTagBlock("x", "p", NewAttrs()), "<p>x</p>"},
		{BuilderTag("turbo_frame", NewAttrs().ID("f")), `<turbo-frame id="f"></turbo-frame>`},
		{BuilderTag("meta", NewAttrs().Name("n")), `<meta name="n">`},
		{LegacyTag("img", NewAttrs().Alt("a")), `<img alt="a" />`},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}

func TestAttributeValuesFromGo(t *testing.T) {
	var none *string
	some := "s'"
	var noneID *int64
	id := int64(7)
	attrs := NewAttrs().
		AttrOpt("a", none).
		Attr("b", int64(-3)).
		Attr("c", 1.5).
		Attr("d", 600.0).
		AttrOpt("e", &some).
		AttrOpt("f", noneID).
		AttrOpt("g", &id).
		Attr("h", RubyFloat(600)).
		Attr("i", uint32(9)).
		Attr("j", false).
		Disabled(false).
		Checked(true)
	attrs.AttrOpt("a", "late")
	if got := renderAttrs(attrs); got != ` a="late" b="-3" c="1.5" d="600" e="s&#39;" g="7" h="600.0" i="9" j="false" checked="checked"` {
		t.Errorf("got %s", got)
	}
	if !attrs.Has("f") || attrs.Len() != 12 {
		t.Errorf("a nil option keeps its key: %v %d", attrs.Has("f"), attrs.Len())
	}
	if _, ok := attrs.Get("f"); ok {
		t.Error("a nil option has no value")
	}
	if got, _ := attrs.Get("j"); got != "false" {
		t.Errorf("Get(j) = %s", got)
	}
	var empty *Attrs
	if got := ContentTag("p", empty, "x"); got != "<p>x</p>" {
		t.Errorf("nil attrs: %s", got)
	}
}
