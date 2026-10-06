package views

import (
	"embed"
	"strconv"
	"strings"
	"sync"
	"time"

	qt "github.com/valyala/quicktemplate"
)

// Views for reference/app/views/messages, plus MessagesHelper, Messages::AttachmentPresentation
// and the boost partials (the reference's messages.rs).

// UserView is what the message views show of a user: avatar_tag and the author heading.
type UserView struct {
	ID   int64
	Name string
	// User#title: name and bio joined by " – ".
	Title string
	// fresh_user_avatar_path(user).
	AvatarURL string
}

func (u *UserView) Path() string { return RouteUser(u.ID) }

type RoomKind uint8

const (
	RoomKindOpen RoomKind = iota
	RoomKindClosed
	RoomKindDirect
)

// ParamKey is Rooms::Open.model_name.param_key, the stem of dom_id(room).
func (k RoomKind) ParamKey() string {
	switch k {
	case RoomKindClosed:
		return "rooms_closed"
	case RoomKindDirect:
		return "rooms_direct"
	}
	return "rooms_open"
}

func (k RoomKind) IsDirect() bool { return k == RoomKindDirect }

// RoomDomID is dom_id(room) / dom_id(room, prefix).
func RoomDomID(kind RoomKind, id int64, prefix string) string {
	if prefix == "" {
		return kind.ParamKey() + "_" + strconv.FormatInt(id, 10)
	}
	return prefix + "_" + kind.ParamKey() + "_" + strconv.FormatInt(id, 10)
}

// MessageView is a message as messages/_message renders it.
type MessageView struct {
	ID int64
	// Message#to_key, so every dom_id(message) uses it.
	ClientMessageID string
	RoomID          int64
	// room_display_name(message.room, for_user: nil); see RoomDisplayName.
	RoomName  string
	Creator   UserView
	CreatedAt time.Time
	UpdatedAt time.Time
	// message.plain_text_body.all_emoji? (reference/lib/rails_ext/string.rb).
	AllEmoji bool
	Content  MessageContent
	// message.boosts.ordered.
	Boosts []BoostView
}

// MessageContent is Message#content_type with what each presentation needs: MessageText,
// *SoundView, *AttachmentView or MessageUnrenderable.
type MessageContent interface{ isMessageContent() }

// MessageText is the presentation filters' output after auto_link, from the richtext package.
type MessageText struct{ HTML string }

// MessageUnrenderable is a message whose rendering raised past message_presentation's own rescue
// (or whose plain_text_body raised): message_tag rescues and renders messages/_unrenderable in
// place of the whole message.
type MessageUnrenderable struct{}

func (MessageText) isMessageContent()         {}
func (*SoundView) isMessageContent()          {}
func (*AttachmentView) isMessageContent()     {}
func (MessageUnrenderable) isMessageContent() {}

// SoundView is a /play <name> message's Sound.
type SoundView struct {
	// asset_path(sound.asset_path), the digested mp3.
	URL   string
	Image *SoundImage
	Text  *string
}

type SoundImage struct {
	// image_path(image.asset_path).
	Src    string
	Width  uint32
	Height uint32
}

// AttachmentView is the message's Active Storage attachment.
type AttachmentView struct {
	// attachment.filename.to_s.
	Filename string
	// rails_blob_path(attachment).
	BlobPath string
	// rails_blob_path(attachment, disposition: "attachment").
	DownloadPath string
	Preview      AttachmentPreview
	// attachment.metadata[:width]: an Integer for images, a Float for videos.
	Width  *RubyNumber
	Height *RubyNumber
}

// AttachmentPreview is how an attachment previews: AttachmentVideo, AttachmentImage or
// AttachmentFile.
type AttachmentPreview interface{ isAttachmentPreview() }

// AttachmentVideo is attachment.video?: url_for(attachment.preview(format: :webp, resize_to_limit: ...)).
type AttachmentVideo struct{ PosterURL string }

// AttachmentImage is otherwise previewable or variable:
// polymorphic_url(attachment.representation(:thumb), only_path: true).
type AttachmentImage struct{ ThumbURL string }

// AttachmentFile is neither previewable nor variable: a download link.
type AttachmentFile struct{}

func (AttachmentVideo) isAttachmentPreview() {}
func (AttachmentImage) isAttachmentPreview() {}
func (AttachmentFile) isAttachmentPreview()  {}

type BoostView struct {
	ID int64
	// For the fragment cache key (boost.cache_key_with_version).
	UpdatedAt time.Time
	MessageID int64
	Content   string
	// boost.content.all_emoji?.
	AllEmoji bool
	Booster  UserView
}

// MessageItem is a message on its way into messages/_message: the fragment itself when the cache
// already holds this message version, else the view to render it from. `cache [ message,
// "presentation-v3" ]` wraps the whole partial, so on a hit Rails evaluates none of it (no rich
// text, attachment, avatar or boosts); CachedMessageFragment lets the presenter look first and
// build a MessageView only on a miss.
type MessageItem struct {
	fragment        *Fragment
	clientMessageID string
	roomID          int64
	view            *MessageView
}

// MessageItemFragment is a message whose fragment the cache already holds.
func MessageItemFragment(clientMessageID string, roomID int64, html *Fragment) MessageItem {
	return MessageItem{fragment: html, clientMessageID: clientMessageID, roomID: roomID}
}

// MessageItemView is a message to render.
func MessageItemView(message *MessageView) MessageItem { return MessageItem{view: message} }

// Fragment is the cached fragment, or nil for a view.
func (m MessageItem) Fragment() *Fragment { return m.fragment }

// View is the view to render, or nil for a cached fragment.
func (m MessageItem) View() *MessageView { return m.view }

// DomID is dom_id(message) / dom_id(message, prefix).
func (m MessageItem) DomID(prefix string) string {
	if m.view != nil {
		return m.view.DomID(prefix)
	}
	if prefix == "" {
		return "message_" + m.clientMessageID
	}
	return prefix + "_message_" + m.clientMessageID
}

func (m MessageItem) RoomID() int64 {
	if m.view != nil {
		return m.view.RoomID
	}
	return m.roomID
}

// Reaction is one of EmojiHelper::REACTIONS.
type Reaction struct{ Character, Title string }

// Reactions is EmojiHelper::REACTIONS.
var Reactions = [8]Reaction{
	{"👍", "Thumbs up"},
	{"👏", "Clapping"},
	{"👋", "Waving hand"},
	{"💪", "Muscle"},
	{"\u2764\ufe0f", "Red heart"},
	{"😂", "Face with tears of joy"},
	{"🎉", "Party popper"},
	{"🔥", "Fire"},
}

// DomID is dom_id(message) / dom_id(message, prefix).
func (m *MessageView) DomID(prefix string) string {
	if prefix == "" {
		return "message_" + m.ClientMessageID
	}
	return prefix + "_message_" + m.ClientMessageID
}

func (m *MessageView) IsUnrenderable() bool {
	_, ok := m.Content.(MessageUnrenderable)
	return ok
}

// Attachment is the message's attachment, or nil.
func (m *MessageView) Attachment() *AttachmentView {
	attachment, _ := m.Content.(*AttachmentView)
	return attachment
}

func (m *MessageView) CreatedAtISO() string  { return ISO8601(m.CreatedAt) }
func (m *MessageView) CreatedAtEpoch() int64 { return EpochMS(m.CreatedAt) }
func (m *MessageView) UpdatedAtEpoch() int64 { return EpochMS(m.UpdatedAt) }
func (m *MessageView) AtPath() string        { return RouteRoomAtMessage(m.RoomID, m.ID) }
func (m *MessageView) Path() string          { return RouteRoomMessage(m.RoomID, m.ID) }
func (m *MessageView) EditPath() string      { return RouteEditRoomMessage(m.RoomID, m.ID) }
func (m *MessageView) BoostsPath() string    { return RouteMessageBoosts(m.ID) }
func (m *MessageView) NewBoostPath() string  { return RouteNewMessageBoost(m.ID) }
func (b *BoostView) DomID() string           { return "boost_" + strconv.FormatInt(b.ID, 10) }
func (b *BoostView) Path() string            { return RouteMessageBoost(b.MessageID, b.ID) }

// MessageEditView is what messages/edit needs besides the message.
type MessageEditView struct {
	Message MessageView
	// The editor's value: editable_body(message) as HTML, from the richtext package.
	EditableBodyHTML string
}

// CachedMessage is `render message`: messages/_message, whose body is `cache [ message,
// "presentation-v3" ]` (and whose collection renders are `cached: true`), so a message version
// renders once. It is also the reference's cached_message, which templates write as text:
// {%s= CachedMessage(ctx, message).HTML %}.
func CachedMessage(ctx *ViewContext, message *MessageView) *Fragment {
	key := appendMessageFragmentKey(make([]byte, 0, 128), message.ID, message.UpdatedAt)
	return ctx.Cache.Fetch(string(key), func() string {
		return renderFragment(8<<10, func(qw *qt.Writer) { StreamMessagesMessage(qw, ctx, message) })
	})
}

// CachedMessageItem is CachedMessage for a MessageItem: a fragment found up front goes out as it
// is. Templates write it with {%= Fragment(CachedMessageItem(ctx, message)) %}, so a page being
// recorded notes where the fragment goes instead of copying it in.
func CachedMessageItem(ctx *ViewContext, item MessageItem) *Fragment {
	if item.fragment != nil {
		return item.fragment
	}
	return CachedMessage(ctx, item.view)
}

// CachedMessageFragment is messages/_message's fragment for this message version, if cache holds
// it. The key needs only the message's id and updated_at.
func CachedMessageFragment(cache *FragmentCache, id int64, updatedAt time.Time) *Fragment {
	if cache == nil {
		return nil
	}
	var key [128]byte
	return cache.fragmentBytes(appendMessageFragmentKey(key[:0], id, updatedAt))
}

// appendMessageFragmentKey appends views/messages/_message:<digest>/messages/<id>-<version>/presentation-v3.
func appendMessageFragmentKey(b []byte, id int64, updatedAt time.Time) []byte {
	b = AppendRecordFragmentKey(b, "messages/_message", messageDigest(), "messages", id, updatedAt)
	return append(b, "/presentation-v3"...)
}

// CachedBoost is messages/boosts/_boost, whose body is `cache boost`. Templates write it with
// {%s= CachedBoost(ctx, &boost).HTML %}.
func CachedBoost(ctx *ViewContext, boost *BoostView) *Fragment {
	key := AppendRecordFragmentKey(make([]byte, 0, 128), "messages/boosts/_boost", boostDigest(), "boosts", boost.ID, boost.UpdatedAt)
	return ctx.Cache.Fetch(string(key), func() string {
		return renderFragment(2<<10, func(qw *qt.Writer) { StreamMessagesBoostsBoost(qw, ctx, boost) })
	})
}

// renderFragment renders a cached partial into a string of exactly its length (the reference's
// fitted): the store keeps it for as long as the fragment lives.
func renderFragment(sizeHint int, render func(qw *qt.Writer)) string {
	return strings.Clone(RenderString(sizeHint, render))
}

// The template sources the fragment keys' digests cover: the partial and what it renders.
//
//go:embed messages_message.qtpl messages_actions.qtpl messages_presentation.qtpl messages_unrenderable.qtpl messages_boosts_boosts.qtpl messages_boosts_boost.qtpl users_sidebars_rooms_direct.qtpl
var digestedTemplates embed.FS

// templateDigest is Digest of the named template files.
func templateDigest(names ...string) string {
	sources := make([]string, len(names))
	for i, name := range names {
		source, err := digestedTemplates.ReadFile(name)
		if err != nil {
			panic(err)
		}
		sources[i] = string(source)
	}
	return Digest(sources...)
}

// messageDigest is the template digest in messages/_message's fragment keys.
var messageDigest = sync.OnceValue(func() string {
	return templateDigest(
		"messages_message.qtpl",
		"messages_actions.qtpl",
		"messages_presentation.qtpl",
		"messages_unrenderable.qtpl",
		"messages_boosts_boosts.qtpl",
		"messages_boosts_boost.qtpl",
	)
})

var boostDigest = sync.OnceValue(func() string { return templateDigest("messages_boosts_boost.qtpl") })
