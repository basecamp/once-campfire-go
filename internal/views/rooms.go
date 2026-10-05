package views

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	qt "github.com/valyala/quicktemplate"
)

// Views for reference/app/views/rooms, plus RoomsHelper, Rooms::InvolvementsHelper and the
// MessagesHelper tags the room screen uses (the reference's rooms.rs).

// RoomDisplayName is room_display_name(room, for_user:): a direct room is named after its other
// members (room.users.without(for_user).pluck(:name).to_sentence), falling back to the user's own
// name when they're alone in it.
func RoomDisplayName(name *string, direct bool, otherMemberNames []string, forUserName *string) string {
	if direct {
		sentence := ToSentence(otherMemberNames, " and ")
		if strings.TrimSpace(sentence) == "" {
			if forUserName == nil {
				return ""
			}
			return *forUserName
		}
		return sentence
	}
	if name == nil {
		return ""
	}
	return *name
}

// MentionPromptSrc is mention_prompt_tag(room)'s src: autocompletable_users_path(room_id: room.id).
func MentionPromptSrc(roomID int64) string {
	return RouteAutocompletableUsers() + "?room_id=" + strconv.FormatInt(roomID, 10)
}

// RoomView is a persisted room.
type RoomView struct {
	ID   int64
	Kind RoomKind
	Name *string
	// room_display_name(room) for Current.user.
	DisplayName string
}

func (r *RoomView) DomID(prefix string) string { return RoomDomID(r.Kind, r.ID, prefix) }

func (r *RoomView) IsDirect() bool { return r.Kind.IsDirect() }

// EditPath is edit_polymorphic_path(room): /rooms/opens/1/edit and so on.
func (r *RoomView) EditPath() string {
	switch r.Kind {
	case RoomKindClosed:
		return RouteEditRoomsClosed(r.ID)
	case RoomKindDirect:
		return RouteEditRoomsDirect(r.ID)
	}
	return RouteEditRoomsOpen(r.ID)
}

// Noun is "Ping" for direct rooms, "room" otherwise.
func (r *RoomView) Noun() string {
	if r.IsDirect() {
		return "Ping"
	}
	return "room"
}

// RoomShowView is what rooms/show shows.
type RoomShowView struct {
	Room RoomView
	// room.updated_at, the refresh controller's loaded_at.
	UpdatedAt time.Time
	// Current.user, for the client-side message template.
	User     UserView
	Messages []MessageItem
	// @room == Room.original && !@room.messages.paged? (rooms/show/_invitation).
	Invitation bool
	// Current.account.join_code, for the invitation's join link.
	JoinCode string
	// Turbo::StreamsChannel.signed_stream_name([room, :messages]).
	MessagesStreamName string
}

// RoomsShow is rooms/show.
type RoomsShow struct {
	PageBase
	Ctx  *ViewContext
	Show *RoomShowView
}

func (p *RoomsShow) PageTitle() *string { return &p.Show.Room.DisplayName }
func (p *RoomsShow) BodyClass() *string { return Ptr("sidebar") }

// LoadedAt is room.updated_at.to_fs(:epoch).
func (p *RoomsShow) LoadedAt() int64 { return EpochMS(p.Show.UpdatedAt) }

// InvolvementView is Membership#involvement.
type InvolvementView struct {
	RoomID int64
	Kind   RoomKind
	// "mentions", "everything", "nothing" or "invisible".
	Involvement string
}

// Button is rooms/involvements/show's button_to_change_involvement.
func (v *InvolvementView) Button(ctx *ViewContext) HTML {
	room := InvolvementRoom{ID: v.RoomID, ParamKey: v.Kind.ParamKey(), Direct: v.Kind.IsDirect()}
	return ButtonToChangeInvolvement(ctx, room, v.Involvement)
}

// RefreshView is what rooms/refreshes/show streams: messages created and updated since the client
// loaded.
type RefreshView struct {
	RoomID          int64
	RoomKind        RoomKind
	NewMessages     []MessageItem
	UpdatedMessages []MessageItem
}

// FormRoom is the room being created or edited by the open and closed room forms. ID is nil for a
// new record.
type FormRoom struct {
	ID   *int64
	Name *string
}

// Action is form_with model: room's action for an open or closed room.
func (r *FormRoom) Action(kind RoomKind) string {
	switch {
	case r.ID != nil && kind == RoomKindOpen:
		return RouteRoomsOpen(*r.ID)
	case r.ID != nil:
		return RouteRoomsClosed(*r.ID)
	case kind == RoomKindOpen:
		return RouteRoomsOpens()
	}
	return RouteRoomsCloseds()
}

func (r *FormRoom) DisplayName() string {
	if r.Name == nil {
		return ""
	}
	return *r.Name
}

// OpenFormView is what rooms/opens/{new,edit} show.
type OpenFormView struct {
	Room FormRoom
	// Current.user.can_administer?(room): administrators, the creator, or a new room.
	CanAdminister bool
	// User.active.ordered.
	Users []UserView
}

// ClosedFormView is what rooms/closeds/{new,edit} show.
type ClosedFormView struct {
	Room          FormRoom
	CanAdminister bool
	CurrentUserID int64
	// Active users with access (none for a new room).
	SelectedUsers []UserView
	// The other active users.
	UnselectedUsers []UserView
}

// RoomFormPage is a page extending rooms/layouts/_new or rooms/layouts/_edit: it fills their
// room_form block.
type RoomFormPage interface {
	StreamRoomForm(qw *qt.Writer)
}

// RoomsLayoutsNew is rooms/layouts/_new, the page the new-room pages extend: its blocks render
// around Page's RoomForm block.
type RoomsLayoutsNew struct {
	PageBase
	Ctx  *ViewContext
	Page RoomFormPage
}

// RoomsLayoutsEdit is rooms/layouts/_edit, the page the room settings pages extend: its blocks
// render around Page's RoomForm block.
type RoomsLayoutsEdit struct {
	PageBase
	Ctx *ViewContext
	// The extending page's form.room and form.can_administer.
	Room          *FormRoom
	CanAdminister bool
	Page          RoomFormPage
}

// RoomID is the edited room's id (self.room_id()).
func (p *RoomsLayoutsEdit) RoomID() int64 {
	if p.Room.ID == nil {
		return 0
	}
	return *p.Room.ID
}

// RoomsOpensNew is rooms/opens/new.
type RoomsOpensNew struct {
	RoomsLayoutsNew
	Form *OpenFormView
}

func NewRoomsOpensNew(ctx *ViewContext, form *OpenFormView) *RoomsOpensNew {
	p := &RoomsOpensNew{Form: form}
	p.RoomsLayoutsNew = RoomsLayoutsNew{Ctx: ctx, Page: p}
	return p
}

func (p *RoomsOpensNew) PageTitle() *string { return Ptr("New chat room") }

// RoomsOpensEdit is rooms/opens/edit.
type RoomsOpensEdit struct {
	RoomsLayoutsEdit
	Form *OpenFormView
}

func NewRoomsOpensEdit(ctx *ViewContext, form *OpenFormView) *RoomsOpensEdit {
	p := &RoomsOpensEdit{Form: form}
	p.RoomsLayoutsEdit = RoomsLayoutsEdit{Ctx: ctx, Room: &form.Room, CanAdminister: form.CanAdminister, Page: p}
	return p
}

func (p *RoomsOpensEdit) PageTitle() *string {
	return Ptr("Edit settings for " + p.Form.Room.DisplayName())
}

// RoomsClosedsNew is rooms/closeds/new.
type RoomsClosedsNew struct {
	RoomsLayoutsNew
	Form *ClosedFormView
}

func NewRoomsClosedsNew(ctx *ViewContext, form *ClosedFormView) *RoomsClosedsNew {
	p := &RoomsClosedsNew{Form: form}
	p.RoomsLayoutsNew = RoomsLayoutsNew{Ctx: ctx, Page: p}
	return p
}

func (p *RoomsClosedsNew) PageTitle() *string { return Ptr("New chat room") }

// RoomsClosedsEdit is rooms/closeds/edit.
type RoomsClosedsEdit struct {
	RoomsLayoutsEdit
	Form *ClosedFormView
}

func NewRoomsClosedsEdit(ctx *ViewContext, form *ClosedFormView) *RoomsClosedsEdit {
	p := &RoomsClosedsEdit{Form: form}
	p.RoomsLayoutsEdit = RoomsLayoutsEdit{Ctx: ctx, Room: &form.Room, CanAdminister: form.CanAdminister, Page: p}
	return p
}

func (p *RoomsClosedsEdit) PageTitle() *string {
	return Ptr("Edit settings for " + p.Form.Room.DisplayName())
}

// RoomsDirectsNew is rooms/directs/new.
type RoomsDirectsNew struct {
	PageBase
	Ctx *ViewContext
}

// DirectEditView is what rooms/directs/edit shows.
type DirectEditView struct {
	RoomID int64
	// room_display_name(@room) for Current.user.
	DisplayName string
	// @room.users.many? ? @room.users.without(Current.user) : @room.users.
	Users []UserView
}

// RoomsDirectsEdit is rooms/directs/edit.
type RoomsDirectsEdit struct {
	PageBase
	Ctx  *ViewContext
	Edit *DirectEditView
}

func (p *RoomsDirectsEdit) PageTitle() *string {
	return Ptr("Edit settings for " + p.Edit.DisplayName)
}

// ButtonToDeleteRoom is button_to_delete_room(room).
func ButtonToDeleteRoom(ctx *ViewContext, roomID int64, displayName string) HTML {
	url := ctx.URL(RouteRoom(roomID))
	content := HTML(`<img aria-hidden="true" src="` + Escape(ctx.Asset("trash.svg")) + `" width="20" height="20" /><span class="overflow-ellipsis">` +
		Escape(displayName) + `</span>`)
	options := NewAttrs().
		Method("delete").
		Class("btn btn--negative max-width").
		Aria("label", "Delete "+displayName).
		Data("turbo_confirm", "Are you sure you want to delete this room and all messages in it? This can’t be undone.")
	return ButtonTo(url, options, content)
}

// Lowercase is Rust's str::to_lowercase, which the room forms' user rows apply to names: full
// Unicode lowercasing, İ to "i̇", and a word-final Σ to ς.
func Lowercase(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, c := range s {
		switch c {
		case 'İ':
			b.WriteString("i̇")
		case 'Σ':
			if casedAcrossIgnorable(s[:i], true) && !casedAcrossIgnorable(s[i+len("Σ"):], false) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(c))
		}
	}
	return b.String()
}

// casedAcrossIgnorable is Rust's case_ignorable_then_cased: skipping case-ignorable characters
// (backwards from the end of s, or forwards from its start), whether the next one is cased.
func casedAcrossIgnorable(s string, backwards bool) bool {
	for len(s) > 0 {
		var c rune
		var size int
		if backwards {
			c, size = utf8.DecodeLastRuneInString(s)
			s = s[:len(s)-size]
		} else {
			c, size = utf8.DecodeRuneInString(s)
			s = s[size:]
		}
		if caseIgnorable(c) {
			continue
		}
		return unicode.IsUpper(c) || unicode.IsLower(c) || unicode.IsTitle(c) ||
			unicode.In(c, unicode.Other_Lowercase, unicode.Other_Uppercase)
	}
	return false
}

// caseIgnorable is Unicode's Case_Ignorable: Mn, Me, Cf, Lm, Sk and the word-break MidLetter,
// MidNumLet and Single_Quote characters.
func caseIgnorable(c rune) bool {
	switch c {
	case '\'', '.', ':', '·', '‘', '’', '․', '‧', '︓', '﹒', '﹕', '＇', '．', '：', '·', '՟', '״':
		return true
	}
	return unicode.In(c, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// RoomForm is the room_form filter, `render layout: "rooms/layouts/form", locals: { room: } do ...
// end`: rooms/layouts/_form around the block's content.
func RoomForm(content HTML, ctx *ViewContext, room *FormRoom, canAdminister bool, kind RoomKind) HTML {
	return HTML(RenderString(len(content)+2048, func(qw *qt.Writer) {
		StreamRoomsLayoutsForm(qw, ctx, room, canAdminister, kind, content)
	}))
}
