package views

import (
	"encoding/base64"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ApplicationHelper, CableHelper, VersionHelper, TimeHelper, ClipboardHelper, DropTargetHelper and
// QrCodeHelper (reference/app/helpers/*.rb).

// PageTitleTag is page_title_tag: `@page_title || "Campfire"`.
func PageTitleTag(pageTitle *string) HTML {
	title := "Campfire"
	if pageTitle != nil {
		title = *pageTitle
	}
	return ContentTagText("title", nil, title)
}

// CurrentUserMetaTags is current_user_meta_tags.
func CurrentUserMetaTags(ctx *ViewContext) HTML {
	user := ctx.CurrentUser
	if user == nil {
		return ""
	}
	return LegacyTag("meta", NewAttrs().Name("current-user-id").Attr("content", user.ID)) +
		LegacyTag("meta", NewAttrs().Name("current-user-name").Attr("content", user.Name))
}

// ScriptAwareActionCableMetaTag is script_aware_action_cable_meta_tag.
func ScriptAwareActionCableMetaTag(ctx *ViewContext) HTML {
	return BuilderTag("meta", NewAttrs().Name("action-cable-url").Attr("content", ctx.CableURL))
}

// CustomStylesTag is custom_styles_tag: the account's CSS, unescaped.
func CustomStylesTag(ctx *ViewContext) HTML {
	if ctx.CustomStyles == nil {
		return ""
	}
	return ContentTag("style", NewAttrs().Data("turbo_track", "reload"), HTML(*ctx.CustomStyles))
}

// BodyClasses is body_classes: `[ @body_class, admin_body_class, account_logo_body_class ].compact.join(" ")`.
func BodyClasses(ctx *ViewContext, bodyClass *string) string {
	classes := make([]string, 0, 3)
	if bodyClass != nil {
		classes = append(classes, *bodyClass)
	}
	if ctx.CanAdminister() {
		classes = append(classes, "admin")
	}
	if ctx.Account.HasLogo {
		classes = append(classes, "account-has-logo")
	}
	return strings.Join(classes, " ")
}

// LinkBack is link_back: to the referrer, unless it's missing or the current page.
func LinkBack(ctx *ViewContext) HTML {
	backURL := RouteRoot()
	if ctx.Referrer != nil && *ctx.Referrer != ctx.RequestURL {
		backURL = *ctx.Referrer
	}
	return LinkBackTo(ctx, backURL)
}

// LinkBackTo is link_back_to(destination).
func LinkBackTo(ctx *ViewContext, destination string) HTML {
	content := ImageTag(ctx, "arrow-left.svg", NewAttrs().AriaHidden().Size(20)) +
		ContentTagText("span", NewAttrs().Class("for-screen-reader"), "Go Back")
	return LinkTo(destination, NewAttrs().Class("btn"), content)
}

// LinkBackToLastRoomVisited is RoomsHelper#link_back_to_last_room_visited.
func LinkBackToLastRoomVisited(ctx *ViewContext) HTML {
	if ctx.LastRoomVisitedID != nil {
		return LinkBackTo(ctx, RouteRoom(*ctx.LastRoomVisitedID))
	}
	return LinkBackTo(ctx, RouteRoot())
}

// VersionBadge is version_badge.
func VersionBadge(ctx *ViewContext) HTML {
	return ContentTagText("span", NewAttrs().Class("version-badge"), ctx.AppVersion)
}

// ButtonToCopyToClipboard is button_to_copy_to_clipboard(url) { content }.
func ButtonToCopyToClipboard(content HTML, url string) HTML {
	options := NewAttrs().
		Class("btn").
		Data("controller", "copy-to-clipboard").
		Data("action", "copy-to-clipboard#copy").
		Data("copy_to_clipboard_success_class", "btn--success").
		Data("copy_to_clipboard_content_value", url)
	return ContentTag("button", options, content)
}

// LinkToZoomQrCode is link_to_zoom_qr_code(url) { content }: the QR code route takes the URL,
// Base64.urlsafe_encode64'd (reference/app/helpers/qr_code_helper.rb).
func LinkToZoomQrCode(content HTML, url string) HTML {
	path := RouteQrCode(base64.URLEncoding.EncodeToString([]byte(url)))
	options := NewAttrs().Class("btn").Data("lightbox_target", "image").Data("action", "lightbox#open").Data("lightbox_url_value", path)
	return LinkTo(path, options, content)
}

// WebShareSessionButton is web_share_session_button(url, title, text) { content } (Users::ProfilesHelper).
func WebShareSessionButton(content HTML, url, title, text string) HTML {
	options := NewAttrs().
		Class("btn").
		Hidden().
		Data("controller", "web-share").
		Data("action", "web-share#share").
		Data("web_share_url_value", url).
		Data("web_share_text_value", text).
		Data("web_share_title_value", title)
	return ContentTag("button", options, content)
}

// Truncate is truncate(text, length:, omission:) with Rails' default of no separator: the result,
// omission included, is at most length characters. Returns plain text (escape on output).
func Truncate(text string, length int, omission string) string {
	if utf8.RuneCountInString(text) <= length {
		return text
	}
	keep := max(length-utf8.RuneCountInString(omission), 0)
	var b strings.Builder
	for _, c := range text {
		if keep == 0 {
			break
		}
		b.WriteRune(c)
		keep--
	}
	b.WriteString(omission)
	return b.String()
}

// Capitalize is String#capitalize as the reference has it: the first character uppercased (by
// full case mapping, so "ß" is "SS"), the rest lowercased.
func Capitalize(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	if size == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text) + 4)
	if upper, ok := multiUppercase[first]; ok {
		b.WriteString(upper)
	} else {
		b.WriteRune(unicode.ToUpper(first))
	}
	for _, c := range text[size:] {
		if c == 'İ' {
			b.WriteString("i\u0307")
		} else {
			b.WriteRune(unicode.ToLower(c))
		}
	}
	return b.String()
}

// multiUppercase is SpecialCasing.txt's unconditional uppercase mappings to more than one
// character, which Rust's char::to_uppercase applies and unicode.ToUpper doesn't. ("İ", lowercased
// to "i̇", is the only such lowercase mapping.)
var multiUppercase = map[rune]string{
	0x00DF: "SS", 0x0149: "\u02BCN", 0x01F0: "J\u030C", 0x0390: "\u0399\u0308\u0301",
	0x03B0: "\u03A5\u0308\u0301", 0x0587: "\u0535\u0552", 0x1E96: "H\u0331", 0x1E97: "T\u0308",
	0x1E98: "W\u030A", 0x1E99: "Y\u030A", 0x1E9A: "A\u02BE", 0x1F50: "\u03A5\u0313",
	0x1F52: "\u03A5\u0313\u0300", 0x1F54: "\u03A5\u0313\u0301", 0x1F56: "\u03A5\u0313\u0342", 0x1F80: "\u1F08\u0399",
	0x1F81: "\u1F09\u0399", 0x1F82: "\u1F0A\u0399", 0x1F83: "\u1F0B\u0399", 0x1F84: "\u1F0C\u0399",
	0x1F85: "\u1F0D\u0399", 0x1F86: "\u1F0E\u0399", 0x1F87: "\u1F0F\u0399", 0x1F88: "\u1F08\u0399",
	0x1F89: "\u1F09\u0399", 0x1F8A: "\u1F0A\u0399", 0x1F8B: "\u1F0B\u0399", 0x1F8C: "\u1F0C\u0399",
	0x1F8D: "\u1F0D\u0399", 0x1F8E: "\u1F0E\u0399", 0x1F8F: "\u1F0F\u0399", 0x1F90: "\u1F28\u0399",
	0x1F91: "\u1F29\u0399", 0x1F92: "\u1F2A\u0399", 0x1F93: "\u1F2B\u0399", 0x1F94: "\u1F2C\u0399",
	0x1F95: "\u1F2D\u0399", 0x1F96: "\u1F2E\u0399", 0x1F97: "\u1F2F\u0399", 0x1F98: "\u1F28\u0399",
	0x1F99: "\u1F29\u0399", 0x1F9A: "\u1F2A\u0399", 0x1F9B: "\u1F2B\u0399", 0x1F9C: "\u1F2C\u0399",
	0x1F9D: "\u1F2D\u0399", 0x1F9E: "\u1F2E\u0399", 0x1F9F: "\u1F2F\u0399", 0x1FA0: "\u1F68\u0399",
	0x1FA1: "\u1F69\u0399", 0x1FA2: "\u1F6A\u0399", 0x1FA3: "\u1F6B\u0399", 0x1FA4: "\u1F6C\u0399",
	0x1FA5: "\u1F6D\u0399", 0x1FA6: "\u1F6E\u0399", 0x1FA7: "\u1F6F\u0399", 0x1FA8: "\u1F68\u0399",
	0x1FA9: "\u1F69\u0399", 0x1FAA: "\u1F6A\u0399", 0x1FAB: "\u1F6B\u0399", 0x1FAC: "\u1F6C\u0399",
	0x1FAD: "\u1F6D\u0399", 0x1FAE: "\u1F6E\u0399", 0x1FAF: "\u1F6F\u0399", 0x1FB2: "\u1FBA\u0399",
	0x1FB3: "\u0391\u0399", 0x1FB4: "\u0386\u0399", 0x1FB6: "\u0391\u0342", 0x1FB7: "\u0391\u0342\u0399",
	0x1FBC: "\u0391\u0399", 0x1FC2: "\u1FCA\u0399", 0x1FC3: "\u0397\u0399", 0x1FC4: "\u0389\u0399",
	0x1FC6: "\u0397\u0342", 0x1FC7: "\u0397\u0342\u0399", 0x1FCC: "\u0397\u0399", 0x1FD2: "\u0399\u0308\u0300",
	0x1FD3: "\u0399\u0308\u0301", 0x1FD6: "\u0399\u0342", 0x1FD7: "\u0399\u0308\u0342", 0x1FE2: "\u03A5\u0308\u0300",
	0x1FE3: "\u03A5\u0308\u0301", 0x1FE4: "\u03A1\u0313", 0x1FE6: "\u03A5\u0342", 0x1FE7: "\u03A5\u0308\u0342",
	0x1FF2: "\u1FFA\u0399", 0x1FF3: "\u03A9\u0399", 0x1FF4: "\u038F\u0399", 0x1FF6: "\u03A9\u0342",
	0x1FF7: "\u03A9\u0342\u0399", 0x1FFC: "\u03A9\u0399", 0xFB00: "FF", 0xFB01: "FI",
	0xFB02: "FL", 0xFB03: "FFI", 0xFB04: "FFL", 0xFB05: "ST",
	0xFB06: "ST", 0xFB13: "\u0544\u0546", 0xFB14: "\u0544\u0535", 0xFB15: "\u0544\u053B",
	0xFB16: "\u054E\u0546", 0xFB17: "\u0544\u053D",
}

// ToSentence is Array#to_sentence with the default English connectors, or a custom
// two_words_connector.
func ToSentence(items []string, twoWordsConnector string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + twoWordsConnector + items[1]
	}
	last := len(items) - 1
	return strings.Join(items[:last], ", ") + ", and " + items[last]
}
