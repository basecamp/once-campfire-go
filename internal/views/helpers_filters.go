package views

import qt "github.com/valyala/quicktemplate"

// Block helpers (`link_to url, class: "btn" do ... end`), the reference's askama filter blocks
// (helpers/filters.rs). A template captures the block with BeginCapture/EndCapture and passes its
// rendered content first:
//
//	link_to(url, options)                        LinkToBlock(content, url, options)
//	button_to(url, options)                      ButtonToBlock(content, url, options)
//	content_tag(name, options)                   ContentTagBlock(content, name, options)
//	form_with(form)                              FormWithBlock(content, form)
//	sidebar_turbo_frame_tag                      SidebarTurboFrameTagBlock(content)
//	button(options)                              Button(content, options)
//	turbo_frame_tag(id, options)                 TurboFrameTag(content, id, options)
//	link_to_room(room_id, options)               LinkToRoom(content, roomID, options)
//	link_to_zoom_qr_code(url)                    LinkToZoomQrCode(content, url)
//	button_to_copy_to_clipboard(url)             ButtonToCopyToClipboard(content, url)
//	web_share_session_button(url, title, text)   WebShareSessionButton(content, url, title, text)
//	user_filter_menu_tag                         UserFilterMenuTag(content)
//
// The Block suffix marks the filters whose names templates also call as plain helpers: LinkTo,
// ButtonTo and ContentTag (content last), FormWith(url) and SidebarTurboFrameTag(src, content). The
// last five live with their helpers, as the reference's content-last versions do.
//
// Block helpers whose output is an opening tag, the content and a closing tag also come as
// EndCapture forms, which end the capture and wrap the content where it was written rather than
// taking it out and writing it back (blockWrap): {% code EndCaptureLinkTo(qw422016, url, options) %}
// in place of {% code captureN := EndCapture(qw422016) %}{%s= string(LinkToBlock(captureN, url,
// options)) %}. The sidebar templates use them.

// LinkToBlock is link_to(url, options) do ... end.
func LinkToBlock(content HTML, url string, options *Attrs) HTML {
	return LinkTo(url, options, content)
}

// ButtonToBlock is button_to(url, options) do ... end; options may include method.
func ButtonToBlock(content HTML, url string, options *Attrs) HTML {
	return ButtonTo(url, options, content)
}

// ContentTagBlock is tag.name(options) do ... end / content_tag(name, options) do ... end.
func ContentTagBlock(content HTML, name string, options *Attrs) HTML {
	return ContentTag(name, options, content)
}

// FormWithBlock is form_with(...) do |form| ... end.
func FormWithBlock(content HTML, form *Form) HTML {
	return form.Wrap(content)
}

// SidebarTurboFrameTagBlock is sidebar_turbo_frame_tag do ... end (the block form never passes src:).
func SidebarTurboFrameTagBlock(content HTML) HTML {
	return SidebarTurboFrameTag(nil, content)
}

// Button is form.button(options) do ... end.
func Button(content HTML, options *Attrs) HTML {
	return ContentTag("button", buttonOptions(options), content)
}

// TurboFrameTag is turbo_frame_tag(id, src:, target:, **attributes) do ... end; src and target may
// be in options and are moved after the id as turbo-rails does.
func TurboFrameTag(content HTML, id string, options *Attrs) HTML {
	src := options.remove("src")
	target := options.remove("target")
	return ContentTag("turbo-frame", turboFrameOptions(id, src, target, options), content)
}

// EndCaptureLinkTo is LinkToBlock around the block just captured, wrapped where it was written.
func EndCaptureLinkTo(qw *qt.Writer, url string, options *Attrs) {
	wrap := wrapBlock(qw)
	openContentTag(&wrap.tag, "a", linkOptions(url, options))
	wrap.close("</a>")
}

// EndCaptureLinkToRoom is LinkToRoom around the block just captured.
func EndCaptureLinkToRoom(qw *qt.Writer, roomID int64, options *Attrs) {
	wrap := wrapBlock(qw)
	openContentTag(&wrap.tag, "a", linkToRoomOptions(roomID, options))
	wrap.close("</a>")
}

// EndCaptureButtonTo is ButtonToBlock around the block just captured.
func EndCaptureButtonTo(qw *qt.Writer, url string, options *Attrs) {
	wrap := wrapBlock(qw)
	openButtonTo(&wrap.tag, url, options)
	wrap.close(buttonToClose)
}

// EndCaptureSidebarTurboFrameTag is SidebarTurboFrameTagBlock around the block just captured.
func EndCaptureSidebarTurboFrameTag(qw *qt.Writer) {
	wrap := wrapBlock(qw)
	openContentTag(&wrap.tag, "turbo-frame", sidebarTurboFrameOptions(nil))
	wrap.close("</turbo-frame>")
}
