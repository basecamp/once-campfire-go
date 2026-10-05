package views

import "testing"

// The presentations as the reference renders them (tests/golden/b/messages_index.json).
func TestPresentsAttachmentsAndSoundsLikeRails(t *testing.T) {
	ctx := testContext()
	ctx.AssetPath = func(logical string) string {
		return map[string]string{
			"common-file-text.svg": "/assets/common-file-text-9043d980.svg",
			"download.svg":         "/assets/download-04029899.svg",
			"share.svg":            "/assets/share-bf28da4f.svg",
		}[logical]
	}
	image := func(width, height RubyNumber) *MessageView {
		return &MessageView{Content: &AttachmentView{BlobPath: "/b.jpg", DownloadPath: "/b.jpg?disposition=attachment",
			Preview: AttachmentImage{ThumbURL: "/t.jpg"}, Width: &width, Height: &height}}
	}
	for _, c := range []struct {
		message *MessageView
		want    HTML
	}{
		// Scaled down to fit 1200x800, so the sizes become Floats.
		{image(RubyInt(3840), RubyInt(2160)), `<div class="max-inline-size center flex overflow-clip" style="width: 600.0px; aspect-ratio: 1.7777777777777777;"><a class="flex" data-lightbox-target="image" data-action="lightbox#open" data-lightbox-url-value="/b.jpg?disposition=attachment" href="/b.jpg"><img width="1200.0" height="675.0" class="message__attachment" loading="lazy" src="/t.jpg" /></a></div>`},
		// Integer halves round down.
		{image(RubyInt(641), RubyInt(640)), `<div class="max-inline-size center flex overflow-clip" style="width: 320px; aspect-ratio: 1.0015625;"><a class="flex" data-lightbox-target="image" data-action="lightbox#open" data-lightbox-url-value="/b.jpg?disposition=attachment" href="/b.jpg"><img width="641" height="640" class="message__attachment" loading="lazy" src="/t.jpg" /></a></div>`},
		{&MessageView{Content: &AttachmentView{BlobPath: "/v.mov", Preview: AttachmentVideo{PosterURL: "/p.webp"}, Width: Ptr(RubyFloat(320)), Height: Ptr(RubyFloat(180))}},
			`<div class="max-inline-size center flex overflow-clip" style="width: 160.0px; aspect-ratio: 1.7777777777777777;"><video src="/v.mov" poster="/p.webp" controls="controls" preload="none" width="100%" height="100%" class="message__attachment"></video></div>`},
		{&MessageView{Content: &AttachmentView{BlobPath: "/v.mov", Preview: AttachmentVideo{PosterURL: "/p.webp"}}},
			`<div class="max-inline-size center overflow-clip"><video src="/v.mov" poster="/p.webp" controls="controls" preload="none" width="100%" height="100%" class="message__attachment"></video></div>`},
		{&MessageView{Content: &AttachmentView{Filename: "notes & -stuff-.txt", DownloadPath: "/n.txt?disposition=attachment", Preview: AttachmentFile{}}},
			`<div class="flex-inline align-center gap-half"><img class="colorize--black" aria-hidden="true" src="/assets/common-file-text-9043d980.svg" width="22" height="22" /><span>notes &amp; -stuff-.txt</span><a class="btn message__action-btn hide-in-ios-pwa" style="--width: auto;" href="/n.txt?disposition=attachment"><img aria-hidden="true" src="/assets/download-04029899.svg" width="20" height="20" /><span class="for-screen-reader">Download notes &amp; -stuff-.txt</span></a><button class="btn message__action-btn" style="--width: auto;" data-controller="web-share" data-action="web-share#share" data-web-share-files-value="/n.txt?disposition=attachment"><img aria-hidden="true" src="/assets/share-bf28da4f.svg" width="20" height="20" /><span class="for-screen-reader">Share notes &amp; -stuff-.txt</span></button></div>`},
		{&MessageView{Content: &SoundView{URL: "/assets/bell-4dd04376.mp3", Text: Ptr("🔔")}},
			`<div class="sound" data-controller="sound" data-action="messages:play-&gt;sound#play" data-sound-url-value="/assets/bell-4dd04376.mp3"><button class="btn btn--plain" data-action="sound#play">🔊</button>🔔</div>`},
		{&MessageView{Content: &SoundView{URL: "/assets/56k-67359aa6.mp3", Image: &SoundImage{Src: "/assets/sounds/56k-1567db5b.webp", Width: 79, Height: 33}}},
			`<div class="sound" data-controller="sound" data-action="messages:play-&gt;sound#play" data-sound-url-value="/assets/56k-67359aa6.mp3"><button class="btn btn--plain" data-action="sound#play">🔊</button><img width="79" height="33" class="align--middle" src="/assets/sounds/56k-1567db5b.webp" /></div>`},
		{&MessageView{Content: MessageText{HTML: "<p>hi</p>"}}, `<p>hi</p>`},
		{&MessageView{Content: MessageUnrenderable{}}, ``},
	} {
		if got := MessagePresentation(ctx, c.message); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}
