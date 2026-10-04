package web

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// renderMessage writes the "message-uncached" template (templates/messages.html)
// without html/template, whose reflection and escaper calls cost more than the rest of
// rendering a message. The reference compiles its templates the same way. Each value is
// escaped as html/template escapes it in that position; TestMessageRendererMatchesTemplate
// compares the two over every branch and adversarial values, so the template remains the
// definition.
type messageRenderer struct {
	secrets     *rails.Secrets
	quickBoosts *quickBoostForms
	assets      map[string]string // escaped asset URLs
	avatars     sync.Map          // user ID -> avatar path without its version
}

// avatar is avatarPath with each user's signed path computed once (the signature
// depends only on the ID and the secrets).
func (r *messageRenderer) avatar(id int64, updated time.Time) string {
	path, ok := r.avatars.Load(id)
	if !ok {
		path, _ = r.avatars.LoadOrStore(id, avatarPath(r.secrets, id, time.Time{}))
	}
	if updated.IsZero() {
		return path.(string)
	}
	return path.(string) + "?v=" + updated.UTC().Format("20060102150405")
}

func newMessageRenderer(secrets *rails.Secrets, quickBoosts *quickBoostForms) *messageRenderer {
	r := &messageRenderer{secrets: secrets, quickBoosts: quickBoosts, assets: map[string]string{}}
	for _, name := range []string{"menu-dots-horizontal.svg", "boost.svg", "download.svg", "share.svg", "reply.svg", "link.svg", "pencil.svg", "minus.svg"} {
		r.assets[name] = urlAttribute(assets.Path(name))
	}
	return r
}

// html/template's escaping of a string in text and quoted attribute values.
func escapeHTML(s string) string { return attributeEscaper.Replace(s) }

// html/template's escaping of a value that starts a URL attribute (href, src):
// unsafe schemes are replaced, then the URL is normalized and attribute-escaped.
func urlAttribute(s string) string {
	if protocol, _, ok := strings.Cut(s, ":"); ok && !strings.Contains(protocol, "/") {
		if !strings.EqualFold(protocol, "http") && !strings.EqualFold(protocol, "https") && !strings.EqualFold(protocol, "mailto") {
			return "#ZgotmplZ"
		}
	}
	return escapeHTML(normalizeURL(s))
}

// html/template's URL normalizer: percent-encodes bytes outside RFC 3986's unreserved
// and reserved sets, keeping existing valid escapes.
func normalizeURL(s string) string {
	var b strings.Builder
	written := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
			continue
		case strings.IndexByte("!#$&*+,/:;=?@[]-._~", c) >= 0:
			continue
		case c == '%' && i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2]):
			continue
		}
		if written == 0 {
			b.Grow(len(s) + 16)
		}
		b.WriteString(s[written:i])
		b.WriteByte('%')
		b.WriteByte("0123456789abcdef"[c>>4])
		b.WriteByte("0123456789abcdef"[c&15])
		written = i + 1
	}
	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}
func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func (r *messageRenderer) render(view messageView) string {
	m := view.Message
	clientID, id, room, creator := escapeHTML(m.ClientID), strconv.FormatInt(m.ID, 10), strconv.FormatInt(m.RoomID, 10), strconv.FormatInt(m.CreatorID, 10)
	created, title := escapeHTML(epochMillis(m.CreatedAt)), escapeHTML(view.CreatorTitle)
	createdISO := escapeHTML(isoTime(m.CreatedAt))
	var b strings.Builder
	b.Grow(6144 + len(view.HTML))
	w := b.WriteString

	w(`<div id="message_`)
	w(clientID)
	w(`" class="message `)
	if view.AllEmoji {
		w(`message--emoji`)
	}
	w(`" data-controller="reply" data-user-id="`)
	w(creator)
	w(`" data-message-id="`)
	w(id)
	w(`" data-message-timestamp="`)
	w(created)
	w(`" data-message-updated-at="`)
	w(escapeHTML(epochMillis(m.UpdatedAt)))
	w(`" data-sort-value="`)
	w(created)
	w(`" data-messages-target="message" data-search-results-target="message" data-refresh-room-target="message" data-reply-composer-outlet="#composer">` + "\n" + `<h2 class="message__day-separator"><time datetime="`)
	w(createdISO)
	w(`" data-local-time-target="date"></time></h2>` + "\n" + `<figure class="avatar message__avatar"><a title="`)
	w(title)
	w(`" class="btn avatar" data-turbo-frame="_top" href="/users/`)
	w(creator)
	w(`"><img aria-hidden="true" src="`)
	w(urlAttribute(r.avatar(m.CreatorID, view.CreatorUpdatedAt)))
	w(`" width="48" height="48"></a></figure>` + "\n" + `<turbo-frame id="edit_message_`)
	w(clientID)
	w(`"><div class="message__body"><div class="message__body-content"><div class="message__meta"><h3 class="message__heading"><span class="message__author" title="`)
	w(title)
	w(`"><strong data-reply-target="author">`)
	w(escapeHTML(m.Creator))
	w(`</strong></span><a target="_top" class="message__permalink" href="/rooms/`)
	w(room)
	w(`/@`)
	w(id)
	w(`"><time class="message__timestamp" datetime="`)
	w(createdISO)
	w(`" data-local-time-target="time"></time></a><span class="message__room"> <a target="_top" data-reply-target="link" href="/rooms/`)
	w(room)
	w(`/@`)
	w(id)
	w(`">`)
	w(escapeHTML(view.RoomName))
	w(`</a></span></h3>` + "\n")

	// message-actions
	w(`<div class="message__actions" data-controller="soft-keyboard"><details class="position-relative" data-controller="popup" data-action="keydown.esc-&gt;popup#close toggle-&gt;popup#toggle click@document-&gt;popup#closeOnClickOutside" data-popup-orientation-top-class="popup-orientation-top"><summary class="btn message__action-btn message__options-btn"><img class="colorize--black" aria-hidden="true" src="`)
	w(r.assets["menu-dots-horizontal.svg"])
	w(`" width="20" height="20"><span class="for-screen-reader">Message options</span></summary><div class="message__actions-menu border shadow" data-popup-target="menu"><div class="quick-boosts">`)
	w(string(r.quickBoosts.render(m.ClientID, m.ID)))
	w(`<a class="btn message__action-btn message__boost-btn" data-turbo-frame="new_boost_message_`)
	w(clientID)
	w(`" data-action="soft-keyboard#open popup#close" href="/messages/`)
	w(id)
	w(`/boosts/new"><img class="colorize--black" aria-hidden="true" src="`)
	w(r.assets["boost.svg"])
	w(`" width="20" height="20"><span class="for-screen-reader">New boost</span></a></div>` + "\n" + `<div class="flex flex-wrap border-top margin-block-start-half pad-block-start-half message__actions-grid">`)
	if view.Attachment != nil {
		w(`<a class="btn message__action-btn center full-width hide-in-ios-pwa" title="Download" aria-label="Download" href="`)
		w(urlAttribute(view.DownloadURL))
		w(`"><img class="colorize--black" aria-hidden="true" src="`)
		w(r.assets["download.svg"])
		w(`" width="20" height="20"></a><button class="btn message__action-btn center full-width" data-controller="web-share" data-action="web-share#share" data-web-share-files-value="`)
		w(escapeHTML(view.BlobURL))
		w(`" data-web-share-title-value="`)
		w(escapeHTML(view.Attachment.Filename))
		w(`" title="Share" aria-label="Share"><img class="colorize--black" aria-hidden="true" src="`)
		w(r.assets["share.svg"])
		w(`" width="20" height="20"></button>`)
	} else {
		w(`<button class="btn message__action-btn center full-width" data-action="reply#reply" title="Reply" aria-label="Reply"><img class="colorize--black" aria-hidden="true" src="`)
		w(r.assets["reply.svg"])
		w(`" width="20" height="20"></button>`)
	}
	w(`<button class="btn message__action-btn center full-width" title="Copy link" aria-label="Copy link" data-controller="copy-to-clipboard" data-action="copy-to-clipboard#copy" data-copy-to-clipboard-success-class="btn--success" data-copy-to-clipboard-content-value="`)
	w(escapeHTML(view.Permalink))
	w(`"><img class="colorize--black" aria-hidden="true" src="`)
	w(r.assets["link.svg"])
	w(`" width="20" height="20"></button><a class="btn message__action-btn center full-width message__edit-btn" data-turbo-frame="edit_message_`)
	w(clientID)
	w(`" title="Edit" aria-label="Edit" href="/rooms/`)
	w(room)
	w(`/messages/`)
	w(id)
	w(`/edit"><img class="colorize--black" aria-hidden="true" src="`)
	w(r.assets["pencil.svg"])
	w(`" width="20" height="20"></a></div></div></details></div></div>` + "\n")

	// presentation
	w(`<div id="presentation_message_`)
	w(clientID)
	w(`" dir="auto" data-reply-target="body" data-messages-target="body">` + "\n  ")
	w(string(view.HTML))
	w("\n</div>\n")

	// boosts
	w(`<turbo-frame id="boosting_message_`)
	w(clientID)
	w(`"><div class="boosts flex flex-wrap align-center gap full-width" style="--column-gap: 0.4ch; --row-gap: 0" data-controller="turbo-streaming" data-action="turbo:submit-start->turbo-streaming#unsubscribe"><div class="flex-inline flex-wrap gap" id="boosts_message_`)
	w(clientID)
	w(`" data-turbo-streaming-target="container">`)
	for _, boost := range view.Boosts {
		boostID, booster := strconv.FormatInt(boost.ID, 10), strconv.FormatInt(boost.BoosterID, 10)
		w(`<div id="boost_`)
		w(boostID)
		w(`" class="boost boost-item flex-inline postion--relative max-width align-center fill-white gap" data-controller="boost-delete" data-boost-delete-perform-class="boost--deleting" data-boost-delete-reveal-class="expanded" data-boost-delete-booster-id-value="`)
		w(booster)
		w(`"><figure class="avatar boost__avatar flex-item-no-shrink"><a title="`)
		w(escapeHTML(boost.BoosterTitle))
		w(`" class="btn avatar" data-turbo-frame="_top" href="/users/`)
		w(booster)
		w(`"><img aria-label="`)
		w(escapeHTML(boost.Booster))
		w(` boosted `)
		w(escapeHTML(boost.Content))
		w(`" src="`)
		w(urlAttribute(r.avatar(boost.BoosterID, boost.BoosterUpdatedAt)))
		w(`" width="48" height="48"></a></figure><span role="button" class="txt-small`)
		if allEmoji(boost.Content) {
			w(` txt-medium`)
		}
		w(`" data-action="click-&gt;boost-delete#reveal keydown.enter-&gt;boost-delete#reveal:prevent" data-boost-delete-target="content">`)
		w(escapeHTML(boost.Content))
		w(`</span><form class="button_to" method="post" action="/messages/`)
		w(strconv.FormatInt(boost.MessageID, 10))
		w(`/boosts/`)
		w(boostID)
		w(`"><input type="hidden" name="_method" value="delete"><button data-action="boost-delete#perform" data-boost-delete-target="button" class="btn btn--negative flex-item-justify-end boost__delete" type="submit"><img aria-hidden="true" src="`)
		w(r.assets["minus.svg"])
		w(`" width="20" height="20"><span class="for-screen-reader">Delete this boost</span></button></form></div><span id="delete_boost_accessible_label" class="for-screen-reader">Press enter to delete this boost</span>`)
	}
	w(`</div><turbo-frame id="new_boost_message_`)
	w(clientID)
	w(`"><div class="flex-inline message__boost-inline" data-controller="soft-keyboard"><a class="boost__action txt-small btn" action="soft-keyboard#open" href="/messages/`)
	w(id)
	w(`/boosts/new"><img aria-hidden="true" src="`)
	w(r.assets["boost.svg"])
	w(`" width="20" height="20"><span class="for-screen-reader">Add a boost</span></a></div></turbo-frame></div></turbo-frame>` + "\n" + `</div></div></turbo-frame></div>`)
	return b.String()
}
