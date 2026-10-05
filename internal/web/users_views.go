package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/useragent"
	"github.com/basecamp/once-campfire-go/internal/views"
	qt "github.com/valyala/quicktemplate"
)

// The users screens as the reference renders them
// (reference/crates/campfire/src/controllers/{users.rs, users/*.rs, autocompletable.rs} and
// presenters/accounts.rs): the sidebar, the profile, push subscriptions, a user's page, the join
// page and the autocompletable users.

var (
	usersSidebarSize           views.RenderSize
	usersProfileSize           views.RenderSize
	usersPushSubscriptionsSize views.RenderSize
	usersShowSize              views.RenderSize
	usersNewSize               views.RenderSize
)

// directPlaceholders is Users::SidebarsController::DIRECT_PLACEHOLDERS.
const directPlaceholders = 20

// transferLinkExpiry is User::Transferable::TRANSFER_LINK_EXPIRY_DURATION.
const transferLinkExpiry = 4 * time.Hour

// userSummary is presenters::user_summary: a User row as the users views see it.
func (s *Server) userSummary(u *database.User) views.UserSummary {
	summary := views.UserSummary{ID: u.ID, Name: u.Name, Role: views.Role(u.Role), Status: views.Status(u.Status), AvatarPath: s.avatarPath(u.ID, u.UpdatedAt)}
	if !u.NullBio {
		summary.Bio = &u.Bio
	}
	if !u.NullEmail {
		summary.EmailAddress = &u.Email
	}
	return summary
}

// mentionUser is presenters::accounts::mention_user.
func (s *Server) mentionUser(u *database.User) views.MentionUser {
	return views.MentionUser{UserSummary: s.userSummary(u), AttachableSgid: s.attachableSgid(u.ID)}
}

// attachableSgid is user.attachable_sgid.
func (s *Server) attachableSgid(id int64) string {
	return s.Secrets.SGID("gid://campfire/User/"+strconv.FormatInt(id, 10)+"?expires_in", "attachable", time.Time{})
}

// transferID is user.transfer_id: signed_id(purpose: :transfer, expires_in: 4.hours).
func (s *Server) transferID(id int64) string {
	return s.Secrets.SignedID("User", id, "transfer", s.DB.Now().Add(transferLinkExpiry))
}

// roomParamKey is Room.model_name.param_key for the room's STI class.
func roomParamKey(roomType string) string {
	switch roomType {
	case "Rooms::Closed":
		return "rooms_closed"
	case "Rooms::Direct":
		return "rooms_direct"
	}
	return "rooms_open"
}

// restrictRoomCreation is Current.account.settings.restrict_room_creation_to_administrators?:
// present? of the stored value (false when the settings don't parse).
func restrictRoomCreation(settings []byte) bool {
	var data map[string]any
	json.Unmarshal(settings, &data)
	switch value := data["restrict_room_creation_to_administrators"].(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	}
	return true
}

// errNoAccount is `Current.account.join_code` on a nil account (NoMethodError).
var errNoAccount = errors.New("undefined method 'join_code' for nil")

// redirectTo is redirect_to: a 302 to the absolute URL of path, with no body.
func (s *Server) redirectTo(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Location", s.origin(r)+path)
	w.WriteHeader(http.StatusFound)
}

// --- Users::SidebarsController ----------------------------------------------------------------

// usersSidebarShow is Users::SidebarsController#show: the room list, loaded into the
// user_sidebar turbo frame.
func (s *Server) usersSidebarShow(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.findTemplate(w, r) {
		return
	}
	ctx := r.Context()
	sidebar, err := s.loadSidebar(ctx, &u)
	if err != nil {
		s.fail(w, err)
		return
	}
	account, found, err := s.DB.AccountFirst(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	restricted := found && restrictRoomCreation(account.Settings)
	page := &views.UsersSidebarsShow{
		CurrentUser:            s.userSummary(&u),
		RoomsStream:            s.Secrets.SignStream("rooms"),
		UserRoomsStream:        s.Secrets.SignStream(rails.UserRoomsStream(u.ID)),
		DirectMemberships:      sidebar.directMemberships,
		DirectPlaceholderUsers: sidebar.directPlaceholderUsers,
		OtherMemberships:       sidebar.otherMemberships,
		CanCreateRooms:         u.Role == 1 || !restricted,
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &usersSidebarSize, func(ctx *views.ViewContext) views.Page {
		page.Ctx = ctx
		return page
	})
}

// sidebarLists is presenters::accounts::Sidebar.
type sidebarLists struct {
	directMemberships      []views.SidebarDirectItem
	otherMemberships       []views.SidebarRoom
	directPlaceholderUsers []views.UserSummary
}

// loadSidebar is presenters::accounts::sidebar. A direct room whose fragment the cache holds for
// this membership version loads none of its members (`cache membership` wraps the partial).
func (s *Server) loadSidebar(ctx context.Context, user *database.User) (*sidebarLists, error) {
	all, err := s.DB.MembershipsVisibleWithOrderedRoom(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	// select { direct }.sort_by { |m| m.room.updated_at }.reverse
	var direct []*database.MembershipRoom
	for i := range all {
		if all[i].Room.Type == "Rooms::Direct" {
			direct = append(direct, &all[i])
		}
	}
	sort.SliceStable(direct, func(i, j int) bool { return direct[i].Room.UpdatedAt.Before(direct[j].Room.UpdatedAt) })
	slices.Reverse(direct)

	lists := &sidebarLists{directMemberships: make([]views.SidebarDirectItem, 0, len(direct))}
	for _, m := range direct {
		if fragment := views.CachedDirectRoomFragment(s.views, m.Membership.ID, m.Membership.UpdatedAt); fragment != nil {
			lists.directMemberships = append(lists.directMemberships, views.SidebarDirectItemFragment(fragment))
			continue
		}
		view, err := s.sidebarDirect(ctx, &m.Membership, &m.Room)
		if err != nil {
			return nil, err
		}
		lists.directMemberships = append(lists.directMemberships, views.SidebarDirectItemView(view))
	}
	for i := range all {
		if m := &all[i]; m.Room.Type != "Rooms::Direct" {
			lists.otherMemberships = append(lists.otherMemberships, views.SidebarRoom{ID: m.Room.ID, ParamKey: roomParamKey(m.Room.Type), Name: m.Room.Name.String, Unread: m.Membership.Unread})
		}
	}
	if lists.directPlaceholderUsers, err = s.directPlaceholderUsers(ctx, user); err != nil {
		return nil, err
	}
	return lists, nil
}

// sidebarDirect is presenters::accounts::sidebar_direct, users/sidebars/rooms/_direct's locals:
// room.users.without(membership.user).presence || [ membership.user ].
func (s *Server) sidebarDirect(ctx context.Context, membership *database.ReferenceMembership, room *database.ReferenceRoom) (*views.SidebarDirect, error) {
	users, err := s.DB.RoomUsers(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	members := make([]views.UserSummary, 0, len(users))
	for i := range users {
		if users[i].ID != membership.UserID {
			members = append(members, s.userSummary(&users[i]))
		}
	}
	if len(members) == 0 {
		own, found, err := s.DB.UserFindByID(ctx, membership.UserID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, database.ErrNoRows
		}
		members = append(members, s.userSummary(&own))
	}
	return &views.SidebarDirect{
		RoomID:              room.ID,
		Unread:              membership.Unread,
		UpdatedAtEpoch:      strconv.FormatInt(views.EpochMS(room.UpdatedAt), 10),
		Members:             members,
		MembershipID:        membership.ID,
		MembershipUpdatedAt: membership.UpdatedAt,
	}, nil
}

// directPlaceholderUsers is find_direct_placeholder_users. The excluded ids are
// `Membership.where(room_id: directs).pluck(:user_id).uniq.including(Current.user.id)`:
// including appends even when the id is already there, and the limit counts that duplicate.
func (s *Server) directPlaceholderUsers(ctx context.Context, user *database.User) ([]views.UserSummary, error) {
	rooms, err := s.DB.RoomsForUserOfType(ctx, user.ID, "Rooms::Direct")
	if err != nil {
		return nil, err
	}
	var exclude []int64
	if len(rooms) > 0 {
		roomIDs := make([]int64, len(rooms))
		for i := range rooms {
			roomIDs[i] = rooms[i].ID
		}
		ids, err := s.DB.MembershipUserIDsInRooms(ctx, roomIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !slices.Contains(exclude, id) {
				exclude = append(exclude, id)
			}
		}
	}
	exclude = append(exclude, user.ID)
	users, err := s.DB.ActiveUsersNotIn(ctx, exclude, max(directPlaceholders-int64(len(exclude)), 0))
	if err != nil {
		return nil, err
	}
	summaries := make([]views.UserSummary, len(users))
	for i := range users {
		summaries[i] = s.userSummary(&users[i])
	}
	return summaries, nil
}

// sharedRoomPartial is users/sidebars/rooms/_shared for room (rooms controllers'
// render_shared_room; presenter.sidebar_room is never unread).
func sharedRoomPartial(room database.ReferenceRoom) string {
	sidebarRoom := views.SidebarRoom{ID: room.ID, ParamKey: roomParamKey(room.Type), Name: room.Name.String}
	return views.RenderString(512, func(qw *qt.Writer) { views.StreamUsersSidebarsRoomsShared(qw, &sidebarRoom) })
}

// directRoomPartial is users/sidebars/rooms/_direct for one membership of a direct room.
type directRoomPartial struct {
	Membership database.ReferenceMembership
	HTML       *views.Fragment
}

// directRoomPartials is users/sidebars/rooms/_direct for each of room's memberships, rendered
// outside a request at base through the fragment cache (Rooms::DirectsController's
// broadcast_create_room, which prepends each to its user's direct_rooms).
func (s *Server) directRoomPartials(ctx context.Context, base string, room database.ReferenceRoom) ([]directRoomPartial, error) {
	viewContext, err := s.detachedViewContext(ctx, base)
	if err != nil {
		return nil, err
	}
	memberships, err := s.DB.MembershipsForRoom(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	partials := make([]directRoomPartial, len(memberships))
	for i := range memberships {
		direct, err := s.sidebarDirect(ctx, &memberships[i], &room)
		if err != nil {
			return nil, err
		}
		partials[i] = directRoomPartial{Membership: memberships[i], HTML: views.CachedDirectRoom(viewContext, direct)}
	}
	return partials, nil
}

// rendererBaseURLFrom is renderer_base_url for the request ctx belongs to (the renderer's
// http://example.org outside one).
func rendererBaseURLFrom(ctx context.Context) string {
	origin, _ := ctx.Value(requestOriginKey{}).(string)
	if origin == "" {
		return "http://example.org"
	}
	scheme, host, _ := strings.Cut(origin, "://")
	if name, _, found := strings.Cut(host, ":"); found && !strings.HasPrefix(host, "[") {
		host = name
	}
	return scheme + "://" + host
}

// --- Users::ProfilesController ----------------------------------------------------------------

// usersProfileShow is Users::ProfilesController#show: the signed-in user's own profile, its
// memberships partitioned into direct and shared rooms.
func (s *Server) usersProfileShow(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.findTemplate(w, r) {
		return
	}
	ctx := r.Context()
	transferID := s.transferID(u.ID)
	_, avatarAttached, err := s.DB.AttachedBlob(ctx, "User", u.ID, "avatar")
	if err != nil {
		s.fail(w, err)
		return
	}
	direct, shared, err := s.profileMemberships(ctx, &u)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := &views.UsersProfilesShow{User: s.userSummary(&u), AvatarAttached: avatarAttached, TransferID: transferID, SharedMemberships: shared, DirectMemberships: direct}
	s.pageOrFrame(w, r, &u, http.StatusOK, &usersProfileSize, func(ctx *views.ViewContext) views.Page {
		page.Ctx = ctx
		return page
	})
}

// profileMemberships is presenters::accounts::profile_memberships:
// `Current.user.memberships.with_ordered_room.partition { |m| m.room.direct? }`.
func (s *Server) profileMemberships(ctx context.Context, user *database.User) (direct, shared []views.ProfileMembership, err error) {
	memberships, err := s.DB.MembershipsWithOrderedRoom(ctx, user.ID)
	if err != nil {
		return nil, nil, err
	}
	for i := range memberships {
		membership, room := &memberships[i].Membership, &memberships[i].Room
		displayName, err := s.roomDisplayNameFor(ctx, room, user)
		if err != nil {
			return nil, nil, err
		}
		isDirect := room.Type == "Rooms::Direct"
		view := views.ProfileMembership{RoomID: room.ID, RoomParamKey: roomParamKey(room.Type), RoomDisplayName: displayName, Involvement: membership.Involvement.String, Direct: isDirect}
		if isDirect {
			direct = append(direct, view)
		} else {
			shared = append(shared, view)
		}
	}
	return direct, shared, nil
}

// roomDisplayNameFor is presenters::accounts::room_display_name: a direct room is named after its
// other members.
func (s *Server) roomDisplayNameFor(ctx context.Context, room *database.ReferenceRoom, user *database.User) (string, error) {
	var name *string
	if room.Name.Valid {
		name = &room.Name.String
	}
	if room.Type != "Rooms::Direct" {
		return views.RoomDisplayName(name, false, nil, &user.Name), nil
	}
	users, err := s.DB.RoomUsers(ctx, room.ID)
	if err != nil {
		return "", err
	}
	var names []string
	for i := range users {
		if users[i].ID != user.ID {
			names = append(names, users[i].Name)
		}
	}
	return views.RoomDisplayName(name, true, names, &user.Name), nil
}

// --- Users::PushSubscriptionsController -------------------------------------------------------

// usersPushSubscriptionsIndex is Users::PushSubscriptionsController#index.
func (s *Server) usersPushSubscriptionsIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.findTemplate(w, r) {
		return
	}
	subscriptions, err := s.DB.PushSubscriptionsForUser(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := &views.UsersPushSubscriptionsIndex{PushSubscriptions: make([]views.PushSubscription, len(subscriptions))}
	for i := range subscriptions {
		page.PushSubscriptions[i] = pushSubscriptionView(&subscriptions[i])
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &usersPushSubscriptionsSize, func(ctx *views.ViewContext) views.Page {
		page.Ctx = ctx
		return page
	})
}

// pushSubscriptionView is presenters::accounts::push_subscription: the subscription with its user
// agent parsed like UserAgent.parse(push_subscription.user_agent).
func pushSubscriptionView(subscription *database.ReferencePushSubscription) views.PushSubscription {
	agent := useragent.Parse(subscription.UserAgent.String)
	return views.PushSubscription{ID: subscription.ID, Endpoint: subscription.Endpoint.String, Browser: agent.Browser.Text, Version: agent.Version.Text, Platform: agent.Platform.Text}
}

// --- UsersController ----------------------------------------------------------------------------

// findUser is `User.find(params[key])`: 404 when there's no such user.
func (s *Server) findUser(w http.ResponseWriter, r *http.Request, key string) (database.User, bool) {
	id, ok := integerCast(r.PathValue(key))
	if !ok {
		publicError(w, r, http.StatusNotFound)
		return database.User{}, false
	}
	user, found, err := s.DB.UserFindByID(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return database.User{}, false
	}
	if !found {
		publicError(w, r, http.StatusNotFound)
		return database.User{}, false
	}
	return user, true
}

// usersShow is UsersController#show.
func (s *Server) usersShow(w http.ResponseWriter, r *http.Request, u database.User) {
	user, ok := s.findUser(w, r, "id")
	if !ok || !s.findTemplate(w, r) {
		return
	}
	page := &views.UsersShow{User: s.userSummary(&user), TransferID: s.transferID(user.ID)}
	s.pageOrFrame(w, r, &u, http.StatusOK, &usersShowSize, func(ctx *views.ViewContext) views.Page {
		page.Ctx = ctx
		return page
	})
}

// usersNew is UsersController#new, the join page: require_unauthenticated_access, then
// verify_join_code.
func (s *Server) usersNew(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnauthenticatedAccess(w, r) {
		return
	}
	account, ok := s.verifyJoinCode(w, r)
	if !ok || !s.findTemplate(w, r) {
		return
	}
	helpContact, err := s.helpContact(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	page := &views.UsersNew{JoinCode: account.JoinCode, HelpContact: helpContact}
	s.pageOrFrame(w, r, nil, http.StatusOK, &usersNewSize, func(ctx *views.ViewContext) views.Page {
		page.Ctx = ctx
		return page
	})
}

// requireUnauthenticatedAccess is require_unauthenticated_access: restore_authentication (the
// session the session_token cookie names, resumed when due), then redirect_signed_in_user_to_root.
// False when it has answered the request.
func (s *Server) requireUnauthenticatedAccess(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie("session_token")
	if err != nil {
		return true
	}
	var token string
	if s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), s.DB.Now(), &token) != nil || token == "" {
		return true
	}
	session, found, err := s.DB.SessionByToken(r.Context(), token)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if !found {
		return true
	}
	if session.NeedsResume(s.DB.Now()) {
		agent, ip := r.UserAgent(), remoteIP(r)
		if err := s.DB.ResumeSession(r.Context(), session, &agent, &ip); err != nil {
			s.fail(w, err)
			return false
		}
		if err := s.setAuthenticationCookie(w, token); err != nil {
			s.fail(w, err)
			return false
		}
	}
	_, found, err = s.DB.UserFindByID(r.Context(), session.UserID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if !found {
		return true
	}
	s.redirectTo(w, r, views.RouteRoot())
	return false
}

// verifyJoinCode is `head :not_found if Current.account.join_code != params[:join_code]`.
func (s *Server) verifyJoinCode(w http.ResponseWriter, r *http.Request) (database.Account, bool) {
	account, found, err := s.DB.AccountFirst(r.Context())
	if err == nil && !found {
		err = errNoAccount
	}
	if err != nil {
		s.fail(w, err)
		return account, false
	}
	if r.PathValue("join_code") != account.JoinCode {
		w.WriteHeader(http.StatusNotFound)
		return account, false
	}
	return account, true
}

// helpContact is presenters::accounts::help_contact: `User.administrator.first`, for
// accounts/_help_contact.
func (s *Server) helpContact(ctx context.Context) (*views.HelpContact, error) {
	name, email, found, err := s.DB.HelpContact(ctx)
	if err != nil || !found {
		return nil, err
	}
	return &views.HelpContact{Name: name, EmailAddress: email.String}, nil
}

// --- Autocompletable::UsersController ---------------------------------------------------------

// autocompletableUsersIndex is Autocompletable::UsersController#index:
// `set_page_and_extract_portion_from find_autocompletable_users.with_attached_avatar.ordered, per_page: 20`.
func (s *Server) autocompletableUsersIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	ctx := r.Context()
	// params[:room_id].present? ? Current.user.rooms.find(params[:room_id]).users : User.all
	var roomID *int64
	if raw := r.Form["room_id"]; len(raw) > 0 && paramPresent(raw[0]) {
		id, ok := integerCast(raw[0])
		if !ok {
			publicError(w, r, http.StatusNotFound)
			return
		}
		room, found, err := s.DB.RoomForUser(ctx, u.ID, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !found {
			publicError(w, r, http.StatusNotFound)
			return
		}
		roomID = &room.ID
	}
	// The rich text editor's mentions prompt filters with `filter`, the autocomplete inputs with `query`.
	var query *string
	for _, key := range []string{"filter", "query"} {
		if raw := r.Form[key]; len(raw) > 0 && paramPresent(raw[0]) {
			query = &raw[0]
			break
		}
	}
	users, err := s.DB.AutocompletableUsers(ctx, roomID, query)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := newGearedPage(r.Form.Get("page"), int64(len(users)), []int64{20})
	portion := page.records(len(users))
	mentions := make([]views.MentionUser, 0, len(portion))
	for _, i := range portion {
		mentions = append(mentions, s.mentionUser(&users[i]))
	}

	format := respondFormat(w, r, "html", "json")
	if format == "" {
		return
	}
	if format == "json" {
		page.applyHeaders(w, s.origin(r)+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(views.AutocompletableUsersIndexJSON(mentions, s.origin(r))))
		return
	}
	// render layout: false: <lexxy-prompt-item> elements for the mentions prompt
	s.bare(w, r, &u, http.StatusOK, "html", "text/html; charset=utf-8", func(ctx *views.ViewContext) *views.RecordedPage {
		return views.Render(0, func(qw *qt.Writer) { views.StreamAutocompletableUsersIndex(qw, ctx, mentions) })
	})
}

// paramPresent is a string param's present?: not empty and not only whitespace.
func paramPresent(value string) bool {
	return strings.TrimFunc(value, isRubyBlank) != ""
}

// isRubyBlank is the whitespace String#blank? skips (/\A[[:space:]]*\z/).
func isRubyBlank(c rune) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return c >= 0x2000 && c <= 0x200a
}

// gearedPage is geared_pagination's page of a recordset with fixed ratios (the reference's
// presenters::pagination::Page): PortionAtOffset, and the JSON headers.
type gearedPage struct {
	number, recordsCount int64
	ratios               []int64
}

// newGearedPage is Recordset.new(records, per_page:).page(params[:page]):
// `param.to_i > 0 ? param.to_i : 1`, capped.
func newGearedPage(param string, recordsCount int64, perPage []int64) *gearedPage {
	number, ok := rubyToI(param)
	if !ok {
		number = 0
		if t := strings.TrimLeft(param, " \t\n\v\f\r"); t != "" && t[0] != '-' {
			number = 1_000_000_000
		}
	}
	return &gearedPage{number: min(max(number, 1), 1_000_000_000), recordsCount: recordsCount, ratios: perPage}
}

func (p *gearedPage) ratio(pageNumber int64) int64 {
	if pageNumber >= 1 && pageNumber <= int64(len(p.ratios)) {
		return p.ratios[pageNumber-1]
	}
	return p.ratios[len(p.ratios)-1]
}

func (p *gearedPage) limit() int64 { return p.ratio(p.number) }

func (p *gearedPage) offset() int64 {
	size := int64(len(p.ratios))
	var variable int64
	for index := range min(p.number-1, size-1) {
		variable += p.ratio(index + 1)
	}
	return variable + max(p.number-size, 0)*p.ratio(size)
}

// nextPage is `page.next_param` unless `page.last?`.
func (p *gearedPage) nextPage() *string {
	if p.number == p.pageCount() {
		return nil
	}
	return views.Ptr(strconv.FormatInt(p.number+1, 10))
}

// records is the indexes of the page's slice of n ordered records.
func (p *gearedPage) records(n int) []int {
	start := min(p.offset(), int64(n))
	end := min(start+p.limit(), int64(n))
	indexes := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		indexes = append(indexes, int(i))
	}
	return indexes
}

func (p *gearedPage) pageCount() int64 {
	var count int64
	residual := p.recordsCount
	for residual > 0 {
		count++
		residual -= p.ratio(count)
	}
	return max(count, 1)
}

// applyHeaders is set_paginated_headers for a JSON request: X-Total-Count, and a Link to the next
// page unless this is the last one.
func (p *gearedPage) applyHeaders(w http.ResponseWriter, requestURL string) {
	w.Header().Set("X-Total-Count", strconv.FormatInt(p.recordsCount, 10))
	if p.number != p.pageCount() {
		w.Header().Set("Link", "<"+urlWithPage(requestURL, strconv.FormatInt(p.number+1, 10))+`>; rel="next"`)
	}
}

// urlWithPage is Addressable's `uri.query_values = (uri.query_values || {}).merge("page" => page)`:
// keys sorted, duplicates collapsed to the last value, components url_encode'd.
func urlWithPage(rawURL, page string) string {
	base, fragment, hasFragment := strings.Cut(rawURL, "#")
	path, query, _ := strings.Cut(base, "?")
	type pair struct {
		key   string
		value *string
	}
	var values []pair
	set := func(key string, value *string) {
		for i := range values {
			if values[i].key == key {
				values[i].value = value
				return
			}
		}
		values = append(values, pair{key, value})
	}
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		if key, value, ok := strings.Cut(part, "="); ok {
			decoded := percentDecodeLossy(strings.ReplaceAll(value, "+", " "))
			set(percentDecodeLossy(key), &decoded)
		} else {
			set(percentDecodeLossy(part), nil)
		}
	}
	set("page", &page)
	sort.SliceStable(values, func(i, j int) bool { return values[i].key < values[j].key })
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = erbURLEncode(v.key)
		if v.value != nil {
			parts[i] += "=" + erbURLEncode(*v.value)
		}
	}
	result := path + "?" + strings.Join(parts, "&")
	if hasFragment {
		result += "#" + fragment
	}
	return result
}

// erbURLEncode is ERB::Util.url_encode: everything but A-Za-z0-9_.-~ percent-encoded, a space as
// %20.
func erbURLEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// percentDecodeLossy is percent_decode_str(..).decode_utf8_lossy(): invalid escapes stay as they
// are, and invalid UTF-8 becomes U+FFFD.
func percentDecodeLossy(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return strings.ToValidUTF8(string(b), "�")
}

func isHex(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}
