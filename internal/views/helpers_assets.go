package views

import (
	"strings"

	qt "github.com/valyala/quicktemplate"
)

// image_tag and asset resolution (AssetTagHelper, AssetUrlHelper).

// AssetPath is asset_path(source): URLs and absolute paths pass through; logical asset paths are
// digested.
func AssetPath(ctx *ViewContext, source string) string {
	if isURL(source) {
		return source
	}
	return ctx.Asset(source)
}

func isURL(source string) bool {
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "data:") || strings.HasPrefix(source, "cid:") {
		return true
	}
	scheme, _, found := strings.Cut(source, "://")
	if !found || scheme == "" {
		return false
	}
	for _, c := range scheme {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

// ImageTag is image_tag(source, options): the options in order, then src, then width/height from
// size: ("20" or "20x30").
func ImageTag(ctx *ViewContext, source string, options *Attrs) HTML {
	var b tagBuilder
	imageTagInto(&b, ctx, source, options)
	return HTML(b.String())
}

// StreamImageTag writes ImageTag's tag into the page: templates write {%= ImageTag(...) %}.
func StreamImageTag(qw *qt.Writer, ctx *ViewContext, source string, options *Attrs) {
	b, w := intoPage(qw)
	imageTagInto(&b, ctx, source, options)
	b.writeOut(qw, w)
}

func imageTagInto(b *tagBuilder, ctx *ViewContext, source string, options *Attrs) {
	size := options.remove("size")
	options = options.put("src", textAttr(AssetPath(ctx, source)))
	if size.kind != noValue {
		s := size.String()
		width, height, found := strings.Cut(s, "x")
		if !found {
			width, height = s, s
		}
		options.put("width", textAttr(width)).put("height", textAttr(height))
	}
	legacyTagInto(b, "img", options)
}
