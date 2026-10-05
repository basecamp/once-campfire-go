package views

import (
	"strings"
	"sync"
	"time"

	qt "github.com/valyala/quicktemplate"
)

// Views for reference/app/views/users (the reference's users.rs).

// UsersNew is users/new.html.erb (the join page).
type UsersNew struct {
	PageBase
	Ctx         *ViewContext
	JoinCode    string
	HelpContact *HelpContact
}

func (p *UsersNew) PageTitle() *string { return Ptr("Sign up") }
func (p *UsersNew) BodyClass() *string { return Ptr("signup") }

// UsersShow is users/show.html.erb.
type UsersShow struct {
	PageBase
	Ctx  *ViewContext
	User UserSummary
	// user.transfer_id, for users/profiles/_transfer (shown to administrators).
	TransferID string
}

func (p *UsersShow) PageTitle() *string { return &p.User.Name }

// MentionUser is a user as users/_mention and the autocompletable views see it.
type MentionUser struct {
	UserSummary
	// user.attachable_sgid.
	AttachableSgid string
}

// ProfileMembership is a membership row on the profile (users/profiles/_membership).
type ProfileMembership struct {
	RoomID int64
	// "rooms_open", "rooms_closed" or "rooms_direct".
	RoomParamKey string
	// room_display_name(membership.room).
	RoomDisplayName string
	Involvement     string
	Direct          bool
}

func (m *ProfileMembership) InvolvementRoom() InvolvementRoom {
	return InvolvementRoom{ID: m.RoomID, ParamKey: m.RoomParamKey, Direct: m.Direct}
}

// UsersProfilesShow is users/profiles/show.html.erb.
type UsersProfilesShow struct {
	PageBase
	Ctx               *ViewContext
	User              UserSummary
	AvatarAttached    bool
	TransferID        string
	SharedMemberships []ProfileMembership
	DirectMemberships []ProfileMembership
}

// ProfileForm is profile_form_with(@user, **params).
func (p *UsersProfilesShow) ProfileForm() *Form {
	return FormWith(RouteUserProfile()).Model("user").Method("patch").Data("controller", "form")
}

func (p *UsersProfilesShow) PageTitle() *string { return &p.User.Name }

// PushSubscription is a Push::Subscription, with its user agent parsed (UserAgent.parse).
type PushSubscription struct {
	ID       int64
	Endpoint string
	Browser  string
	Version  string
	Platform string
}

// UsersPushSubscriptionsIndex is users/push_subscriptions/index.html.erb.
type UsersPushSubscriptionsIndex struct {
	PageBase
	Ctx               *ViewContext
	PushSubscriptions []PushSubscription
}

func (p *UsersPushSubscriptionsIndex) PageTitle() *string {
	return Ptr("Push notification subscriptions")
}

// SidebarDirect is a direct room in the sidebar (users/sidebars/rooms/_direct).
type SidebarDirect struct {
	RoomID int64
	Unread bool
	// room.updated_at.to_fs(:epoch).
	UpdatedAtEpoch string
	// room.users.without(membership.user).presence || [ membership.user ], in that order.
	Members []UserSummary
	// The membership's id and updated_at: the partial is `cache membership`.
	MembershipID        int64
	MembershipUpdatedAt time.Time
}

// SidebarDirectItem is a direct room on its way into the sidebar: the users/sidebars/rooms/_direct
// fragment when the cache already holds this membership version (`cache membership` wraps the
// whole partial, so Rails evaluates none of it then), else the view to render it from.
type SidebarDirectItem struct {
	fragment *Fragment
	view     *SidebarDirect
}

// SidebarDirectItemFragment is a direct room whose fragment the cache already holds.
func SidebarDirectItemFragment(html *Fragment) SidebarDirectItem {
	return SidebarDirectItem{fragment: html}
}

// SidebarDirectItemView is a direct room to render.
func SidebarDirectItemView(membership *SidebarDirect) SidebarDirectItem {
	return SidebarDirectItem{view: membership}
}

// Fragment is the cached fragment, or nil for a view.
func (d SidebarDirectItem) Fragment() *Fragment { return d.fragment }

// View is the view to render, or nil for a cached fragment.
func (d SidebarDirectItem) View() *SidebarDirect { return d.view }

// CachedDirectRoom is users/sidebars/rooms/_direct for membership, whose body is `cache
// membership` (and which users/sidebars/show renders with cached: true): the first rendering of a
// membership version is what later renders reuse.
func CachedDirectRoom(ctx *ViewContext, membership *SidebarDirect) *Fragment {
	key := appendDirectRoomFragmentKey(make([]byte, 0, 128), membership.MembershipID, membership.MembershipUpdatedAt)
	return ctx.Cache.Fetch(string(key), func() string {
		return renderFragment(2<<10, func(qw *qt.Writer) { StreamUsersSidebarsRoomsDirect(qw, ctx, membership) })
	})
}

// CachedDirectRoomItem is CachedDirectRoom for a SidebarDirectItem (the reference's
// cached_direct_room), which templates write as text: {%s= CachedDirectRoomItem(ctx, membership).HTML %}.
func CachedDirectRoomItem(ctx *ViewContext, item SidebarDirectItem) *Fragment {
	if item.fragment != nil {
		return item.fragment
	}
	return CachedDirectRoom(ctx, item.view)
}

// CachedDirectRoomFragment is the users/sidebars/rooms/_direct fragment for this membership
// version, if cache holds it.
func CachedDirectRoomFragment(cache *FragmentCache, membershipID int64, updatedAt time.Time) *Fragment {
	if cache == nil {
		return nil
	}
	return cache.Fragment(string(appendDirectRoomFragmentKey(make([]byte, 0, 128), membershipID, updatedAt)))
}

func appendDirectRoomFragmentKey(b []byte, membershipID int64, updatedAt time.Time) []byte {
	return AppendRecordFragmentKey(b, "users/sidebars/rooms/_direct", directRoomDigest(), "memberships", membershipID, updatedAt)
}

var directRoomDigest = sync.OnceValue(func() string { return templateDigest("users_sidebars_rooms_direct.qtpl") })

func (d *SidebarDirect) ClassNames() string {
	if d.Unread {
		return "direct unread"
	}
	return "direct"
}

// MemberInitials is
// members.map { |m| m.name.split(' ')[0, 3].map { |s| s[0].capitalize }.join }.to_sentence(two_words_connector: '+').
func (d *SidebarDirect) MemberInitials() string {
	initials := make([]string, len(d.Members))
	for i := range d.Members {
		var b strings.Builder
		for j, part := range d.Members[i].NameParts() {
			if j == 3 {
				break
			}
			for _, c := range part {
				b.WriteString(Capitalize(string(c)))
				break
			}
		}
		initials[i] = b.String()
	}
	return ToSentence(initials, "+")
}

// SidebarRoom is a shared room in the sidebar (users/sidebars/rooms/_shared).
type SidebarRoom struct {
	ID int64
	// "rooms_open" or "rooms_closed".
	ParamKey string
	Name     string
	Unread   bool
}

func (r *SidebarRoom) ClassNames() string {
	if r.Unread {
		return "align-center gap room btn txt-nowrap unread"
	}
	return "align-center gap room btn txt-nowrap"
}

// UsersSidebarsShow is users/sidebars/show.html.erb.
type UsersSidebarsShow struct {
	PageBase
	Ctx         *ViewContext
	CurrentUser UserSummary
	// Turbo::StreamsChannel.signed_stream_name(:rooms).
	RoomsStream string
	// Turbo::StreamsChannel.signed_stream_name([ Current.user, :rooms ]).
	UserRoomsStream        string
	DirectMemberships      []SidebarDirectItem
	DirectPlaceholderUsers []UserSummary
	OtherMemberships       []SidebarRoom
	// Current.user.administrator? || !Current.account.settings.restrict_room_creation_to_administrators?.
	CanCreateRooms bool
}
