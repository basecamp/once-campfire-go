package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Plain messages are the write-path hot case: no attachment and no boosts.
// The bytes match message-uncached so the fragment cache stays interchangeable
// with html/template. Anything else still goes through the template.
func plainMessage(v messageView) bool {
	return v.Attachment == nil && len(v.Boosts) == 0
}

func (s *Server) renderPlainMessage(v messageView) string {
	var b strings.Builder
	b.Grow(2048)
	e := templateEscape
	client := e(v.ClientID)
	id := strconv.FormatInt(v.ID, 10)
	creator := strconv.FormatInt(v.CreatorID, 10)
	room := strconv.FormatInt(v.RoomID, 10)
	created := epochMillis(v.CreatedAt)
	updated := epochMillis(v.UpdatedAt)
	iso := v.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	title := e(v.CreatorTitle)
	name := e(v.Creator)
	roomName := e(v.RoomName)
	avatar := e(avatarPath(s.Secrets, v.CreatorID, v.CreatorUpdatedAt))
	permalink := e(v.Permalink)
	class := "message "
	if v.AllEmoji {
		class = "message message--emoji"
	}
	b.WriteString(`<div id="message_`)
	b.WriteString(client)
	b.WriteString(`" class="`)
	b.WriteString(class)
	b.WriteString(`" data-controller="reply" data-user-id="`)
	b.WriteString(creator)
	b.WriteString(`" data-message-id="`)
	b.WriteString(id)
	b.WriteString(`" data-message-timestamp="`)
	b.WriteString(created)
	b.WriteString(`" data-message-updated-at="`)
	b.WriteString(updated)
	b.WriteString(`" data-sort-value="`)
	b.WriteString(created)
	b.WriteString(`" data-messages-target="message" data-search-results-target="message" data-refresh-room-target="message" data-reply-composer-outlet="#composer">`)
	b.WriteByte('\n')
	b.WriteString(`<h2 class="message__day-separator"><time datetime="`)
	b.WriteString(iso)
	b.WriteString(`" data-local-time-target="date"></time></h2>`)
	b.WriteByte('\n')
	b.WriteString(`<figure class="avatar message__avatar"><a title="`)
	b.WriteString(title)
	b.WriteString(`" class="btn avatar" data-turbo-frame="_top" href="/users/`)
	b.WriteString(creator)
	b.WriteString(`"><img aria-hidden="true" src="`)
	b.WriteString(avatar)
	b.WriteString(`" width="48" height="48"></a></figure>`)
	b.WriteByte('\n')
	b.WriteString(`<turbo-frame id="edit_message_`)
	b.WriteString(client)
	b.WriteString(`"><div class="message__body"><div class="message__body-content"><div class="message__meta"><h3 class="message__heading"><span class="message__author" title="`)
	b.WriteString(title)
	b.WriteString(`"><strong data-reply-target="author">`)
	b.WriteString(name)
	b.WriteString(`</strong></span><a target="_top" class="message__permalink" href="/rooms/`)
	b.WriteString(room)
	b.WriteString(`/@`)
	b.WriteString(id)
	b.WriteString(`"><time class="message__timestamp" datetime="`)
	b.WriteString(iso)
	b.WriteString(`" data-local-time-target="time"></time></a><span class="message__room"> <a target="_top" data-reply-target="link" href="/rooms/`)
	b.WriteString(room)
	b.WriteString(`/@`)
	b.WriteString(id)
	b.WriteString(`">`)
	b.WriteString(roomName)
	b.WriteString(`</a></span></h3>`)
	b.WriteByte('\n')
	writeMessageActions(&b, v, client, id, room, permalink)
	b.WriteString(`</div>`)
	b.WriteByte('\n')
	b.WriteString(`<div id="presentation_message_`)
	b.WriteString(client)
	b.WriteString(`" dir="auto" data-reply-target="body" data-messages-target="body">`)
	b.WriteByte('\n')
	b.WriteString(`  `)
	b.WriteString(string(v.HTML))
	b.WriteByte('\n')
	b.WriteString(`</div>`)
	b.WriteByte('\n')
	b.WriteString(`<turbo-frame id="boosting_message_`)
	b.WriteString(client)
	b.WriteString(`"><div class="boosts flex flex-wrap align-center gap full-width" style="--column-gap: 0.4ch; --row-gap: 0" data-controller="turbo-streaming" data-action="turbo:submit-start->turbo-streaming#unsubscribe"><div class="flex-inline flex-wrap gap" id="boosts_message_`)
	b.WriteString(client)
	b.WriteString(`" data-turbo-streaming-target="container"></div><turbo-frame id="new_boost_message_`)
	b.WriteString(client)
	b.WriteString(`"><div class="flex-inline message__boost-inline" data-controller="soft-keyboard"><a class="boost__action txt-small btn" action="soft-keyboard#open" href="/messages/`)
	b.WriteString(id)
	b.WriteString(`/boosts/new"><img aria-hidden="true" src="`)
	b.WriteString(assetBoost)
	b.WriteString(`" width="20" height="20"><span class="for-screen-reader">Add a boost</span></a></div></turbo-frame></div></turbo-frame>`)
	b.WriteByte('\n')
	b.WriteString(`</div></div></turbo-frame></div>`)
	return b.String()
}

var (
	assetDots   = assets.Path("menu-dots-horizontal.svg")
	assetBoost  = assets.Path("boost.svg")
	assetReply  = assets.Path("reply.svg")
	assetLink   = assets.Path("link.svg")
	assetPencil = assets.Path("pencil.svg")
)

func writeMessageActions(b *strings.Builder, v messageView, client, id, room, permalink string) {
	b.WriteString(`<div class="message__actions" data-controller="soft-keyboard"><details class="position-relative" data-controller="popup" data-action="keydown.esc-&gt;popup#close toggle-&gt;popup#toggle click@document-&gt;popup#closeOnClickOutside" data-popup-orientation-top-class="popup-orientation-top"><summary class="btn message__action-btn message__options-btn"><img class="colorize--black" aria-hidden="true" src="`)
	b.WriteString(assetDots)
	b.WriteString(`" width="20" height="20"><span class="for-screen-reader">Message options</span></summary><div class="message__actions-menu border shadow" data-popup-target="menu"><div class="quick-boosts">`)
	for _, reaction := range reactions {
		character := templateEscape(reaction.Character)
		title := templateEscape(reaction.Title)
		b.WriteString(`<form data-turbo-frame="boosting_message_`)
		b.WriteString(client)
		b.WriteString(`" data-action="popup#close" action="/messages/`)
		b.WriteString(id)
		b.WriteString(`/boosts" accept-charset="UTF-8" method="post"><input type="hidden" id="boost_content" name="boost[content]" value="`)
		b.WriteString(character)
		b.WriteString(`"><button name="button" type="submit" title="`)
		b.WriteString(title)
		b.WriteString(`" class="btn message__action-btn" data-emoji="`)
		b.WriteString(character)
		b.WriteString(`"><figure class="margin-none boost-character">`)
		b.WriteString(character)
		b.WriteString(`</figure><span class="for-screen-reader">`)
		b.WriteString(title)
		b.WriteString(`</span></button></form>`)
	}
	b.WriteString(`<a class="btn message__action-btn message__boost-btn" data-turbo-frame="new_boost_message_`)
	b.WriteString(client)
	b.WriteString(`" data-action="soft-keyboard#open popup#close" href="/messages/`)
	b.WriteString(id)
	b.WriteString(`/boosts/new"><img class="colorize--black" aria-hidden="true" src="`)
	b.WriteString(assetBoost)
	b.WriteString(`" width="20" height="20"><span class="for-screen-reader">New boost</span></a></div>`)
	b.WriteByte('\n')
	b.WriteString(`<div class="flex flex-wrap border-top margin-block-start-half pad-block-start-half message__actions-grid"><button class="btn message__action-btn center full-width" data-action="reply#reply" title="Reply" aria-label="Reply"><img class="colorize--black" aria-hidden="true" src="`)
	b.WriteString(assetReply)
	b.WriteString(`" width="20" height="20"></button><button class="btn message__action-btn center full-width" title="Copy link" aria-label="Copy link" data-controller="copy-to-clipboard" data-action="copy-to-clipboard#copy" data-copy-to-clipboard-success-class="btn--success" data-copy-to-clipboard-content-value="`)
	b.WriteString(permalink)
	b.WriteString(`"><img class="colorize--black" aria-hidden="true" src="`)
	b.WriteString(assetLink)
	b.WriteString(`" width="20" height="20"></button><a class="btn message__action-btn center full-width message__edit-btn" data-turbo-frame="edit_message_`)
	b.WriteString(client)
	b.WriteString(`" title="Edit" aria-label="Edit" href="/rooms/`)
	b.WriteString(room)
	b.WriteString(`/messages/`)
	b.WriteString(id)
	b.WriteString(`/edit"><img class="colorize--black" aria-hidden="true" src="`)
	b.WriteString(assetPencil)
	b.WriteString(`" width="20" height="20"></a></div></div></details></div>`)
}

func templateEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func epochMillis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func avatarPath(secrets *rails.Secrets, id int64, updated time.Time) string {
	token := secrets.SignedID("User", id, "avatar", time.Time{})
	path := "/users/" + token + "/avatar"
	if !updated.IsZero() {
		path += "?v=" + updated.UTC().Format("20060102150405")
	}
	return path
}
