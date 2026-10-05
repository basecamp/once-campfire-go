package views

import (
	"math"
	"strconv"
)

// MessagesHelper#message_presentation and Messages::AttachmentPresentation
// (reference/app/helpers/messages_helper.rb, reference/app/helpers/messages/attachment_presentation.rb).

// Message::THUMBNAIL_MAX_WIDTH / THUMBNAIL_MAX_HEIGHT.
const (
	thumbnailMaxWidth  = 1200
	thumbnailMaxHeight = 800
)

// MessagePresentation is message_presentation(message).
func MessagePresentation(ctx *ViewContext, message *MessageView) HTML {
	switch content := message.Content.(type) {
	case *AttachmentView:
		return AttachmentPresentation(ctx, content)
	case *SoundView:
		return soundPresentation(content)
	case MessageText:
		return HTML(content.HTML)
	}
	// MessageUnrenderable: messages/_message renders messages/_unrenderable instead.
	return ""
}

// soundPresentation is message_sound_presentation: a play button followed by the sound's image or
// text.
func soundPresentation(sound *SoundView) HTML {
	var content string
	switch {
	case sound.Image != nil:
		content = `<img width="` + strconv.FormatUint(uint64(sound.Image.Width), 10) + `" height="` +
			strconv.FormatUint(uint64(sound.Image.Height), 10) + `" class="align--middle" src="` + Escape(sound.Image.Src) + `" />`
	case sound.Text != nil:
		content = Escape(*sound.Text)
	}
	return HTML(`<div class="sound" data-controller="sound" data-action="messages:play-&gt;sound#play" data-sound-url-value="` +
		Escape(sound.URL) + `"><button class="btn btn--plain" data-action="sound#play">🔊</button>` + content + `</div>`)
}

// AttachmentPresentation is Messages::AttachmentPresentation#render.
func AttachmentPresentation(ctx *ViewContext, attachment *AttachmentView) HTML {
	switch preview := attachment.Preview.(type) {
	case AttachmentVideo:
		return videoPreview(attachment, preview.PosterURL)
	case AttachmentImage:
		return lightboxedImagePreview(attachment, preview.ThumbURL)
	}
	return fileLink(ctx, attachment)
}

func videoPreview(attachment *AttachmentView, posterURL string) HTML {
	video := `<video src="` + Escape(attachment.BlobPath) + `" poster="` + Escape(posterURL) +
		`" controls="controls" preload="none" width="100%" height="100%" class="message__attachment"></video>`
	width, height, ok := previewDimensions(attachment)
	return inlineMediaDimensionConstraints(width, height, ok, video)
}

func lightboxedImagePreview(attachment *AttachmentView, thumbURL string) HTML {
	width, height, ok := previewDimensions(attachment)
	size := ""
	if ok {
		size = ` width="` + width.String() + `" height="` + height.String() + `"`
	}
	image := `<img` + size + ` class="message__attachment" loading="lazy" src="` + Escape(thumbURL) + `" />`
	link := `<a class="flex" data-lightbox-target="image" data-action="lightbox#open" data-lightbox-url-value="` +
		Escape(attachment.DownloadPath) + `" href="` + Escape(attachment.BlobPath) + `">` + image + `</a>`
	return inlineMediaDimensionConstraints(width, height, ok, link)
}

// inlineMediaDimensionConstraints wraps content in a box of the preview's size, when known (ok).
func inlineMediaDimensionConstraints(width, height RubyNumber, ok bool, content string) HTML {
	if !ok {
		return HTML(`<div class="max-inline-size center overflow-clip">` + content + `</div>`)
	}
	aspectRatio := RubyFloat(width.ToF() / height.ToF())
	return HTML(`<div class="max-inline-size center flex overflow-clip" style="width: ` + width.Half().String() +
		`px; aspect-ratio: ` + aspectRatio.String() + `;">` + content + `</div>`)
}

// previewDimensions is preview_dimensions: the metadata size, scaled down to fit the thumbnail
// bounds; ok is false without both.
func previewDimensions(attachment *AttachmentView) (width, height RubyNumber, ok bool) {
	if attachment.Width == nil || attachment.Height == nil {
		return RubyNumber{}, RubyNumber{}, false
	}
	width, height = *attachment.Width, *attachment.Height
	if width.ToF() <= thumbnailMaxWidth && height.ToF() <= thumbnailMaxHeight {
		return width, height, true
	}
	widthFactor := thumbnailMaxWidth / width.ToF()
	heightFactor := thumbnailMaxHeight / height.ToF()
	// f64::min: a NaN argument yields the other one.
	scale := widthFactor
	if heightFactor < widthFactor || math.IsNaN(widthFactor) {
		scale = heightFactor
	}
	return RubyFloat(width.ToF() * scale), RubyFloat(height.ToF() * scale), true
}

// fileLink is render_link: file icon, name, download link and share button, with no whitespace
// between.
func fileLink(ctx *ViewContext, attachment *AttachmentView) HTML {
	filename := Escape(attachment.Filename)
	download := Escape(attachment.DownloadPath)
	return HTML(`<div class="flex-inline align-center gap-half">` +
		`<img class="colorize--black" aria-hidden="true" src="` + Escape(ctx.Asset("common-file-text.svg")) + `" width="22" height="22" />` +
		`<span>` + filename + `</span>` +
		`<a class="btn message__action-btn hide-in-ios-pwa" style="--width: auto;" href="` + download + `">` +
		`<img aria-hidden="true" src="` + Escape(ctx.Asset("download.svg")) + `" width="20" height="20" />` +
		`<span class="for-screen-reader">Download ` + filename + `</span></a>` +
		`<button class="btn message__action-btn" style="--width: auto;" data-controller="web-share" data-action="web-share#share" data-web-share-files-value="` + download + `">` +
		`<img aria-hidden="true" src="` + Escape(ctx.Asset("share.svg")) + `" width="20" height="20" />` +
		`<span class="for-screen-reader">Share ` + filename + `</span></button>` +
		`</div>`)
}
