package views

import "strconv"

// turbo-rails helpers: turbo_frame_tag, turbo_stream_from, turbo_page_requires_reload.

// turboFrameOptions is turbo_frame_tag(id, src:, target:, **attributes) { content }'s attributes:
// the given ones first, then id, src and target (nil ones dropped).
func turboFrameOptions(id string, src, target attrValue, attributes *Attrs) *Attrs {
	return attributes.put("id", textAttr(id)).put("src", src.asText()).put("target", target.asText())
}

// TurboStreamFrom is turbo_stream_from(*streamables). The signed stream name comes from the caller
// (Turbo::StreamsChannel.signed_stream_name).
func TurboStreamFrom(signedStreamName string) HTML {
	return BuilderTag("turbo-cable-stream-source",
		NewAttrs().Attr("channel", "Turbo::StreamsChannel").Attr("signed-stream-name", signedStreamName))
}

// TurboPageRequiresReloadTag is turbo_page_requires_reload_tag, which turbo_page_requires_reload
// provides to :head.
func TurboPageRequiresReloadTag() HTML {
	return BuilderTag("meta", NewAttrs().Name("turbo-visit-control").Attr("content", "reload"))
}

// DomID is dom_id(record, prefix): "prefix_model_id", or "model_id" without a prefix.
func DomID(model string, id int64, prefix ...string) string {
	s := model + "_" + strconv.FormatInt(id, 10)
	if len(prefix) > 0 {
		return prefix[0] + "_" + s
	}
	return s
}
