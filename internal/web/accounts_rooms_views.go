package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/views"
	qt "github.com/valyala/quicktemplate"
)

// Account settings, chat bots and the room forms, as the reference serves them
// (reference/crates/campfire/src/controllers/{accounts.rs,accounts/**,rooms/{opens,closeds,directs}.rs}
// and presenters/accounts.rs).

var (
	accountsEditSize, customStylesEditSize, botsIndexSize, botsNewSize, botsEditSize views.RenderSize
	opensNewSize, opensEditSize, closedsNewSize, closedsEditSize                     views.RenderSize
	directsNewSize, directsEditSize                                                  views.RenderSize
)

// accountsUsersPerPage is `set_page_and_extract_portion_from users, per_page: 500`.
const accountsUsersPerPage = 500

// turboStreamContentType is the turbo_stream format's Content-Type.
const turboStreamContentType = "text/vnd.turbo-stream.html; charset=utf-8"

// --- AccountsController -------------------------------------------------------------------------

// accountsEdit is AccountsController#edit: everyone, administrators first; the page only decides
// whether a next-page loader follows.
func (s *Server) accountsEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	account, ok := s.currentAccount(w, r)
	if !ok || !s.findTemplate(w, r) {
		return
	}
	canAdminister := u.Role == 1
	users, err := s.DB.UsersForAccount(r.Context(), canAdminister)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := newGearedPage(r, int64(len(users)), accountsUsersPerPage)
	var administrators, members []views.UserSummary
	for _, user := range users {
		summary := s.presentUserSummary(user)
		if summary.Administrator() {
			administrators = append(administrators, summary)
		} else {
			members = append(members, summary)
		}
	}
	restrict := database.AccountSettingsRestrictRoomCreation(account.Settings)
	s.pageOrFrame(w, r, &u, http.StatusOK, &accountsEditSize, func(ctx *views.ViewContext) views.Page {
		return &views.AccountsEdit{Ctx: ctx, AccountID: account.ID, JoinCode: account.JoinCode,
			RestrictRoomCreationToAdministrators: restrict, Administrators: administrators, Members: members, NextPage: page.nextPage()}
	})
}

// accountsUpdate is AccountsController#update:
// `@account.update!(params.require(:account).permit(:name, :logo, settings: {}))`.
func (s *Server) accountsUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	account, ok := s.currentAccount(w, r)
	if !ok || !requireParam(w, r, "account") {
		return
	}
	name := scalarParam(r, "account[name]")
	var settings [][2]string
	for key, values := range r.Form {
		setting, found := strings.CutPrefix(key, "account[settings][")
		if !found {
			continue
		}
		setting, nested, _ := strings.Cut(setting, "]")
		value := ""
		if nested == "" && len(values) > 0 && !nullParam(r, key) {
			value = values[len(values)-1]
		}
		settings = append(settings, [2]string{setting, value})
	}
	upload, err := s.optionalUpload(r, "account[logo]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.AccountUpdate(r.Context(), account, name, nil, settings, recordAttachment(r, "account[logo]", upload, false)); err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	s.flash(r, "notice", "✓")
	s.redirectToPath(w, r, views.RouteEditAccount())
}

// currentAccount is `Current.account` where the reference dereferences it: a missing account is
// a 500.
func (s *Server) currentAccount(w http.ResponseWriter, r *http.Request) (database.Account, bool) {
	account, found, err := s.DB.AccountFirst(r.Context())
	if err == nil && !found {
		err = errors.New("no account")
	}
	if err != nil {
		s.fail(w, err)
		return account, false
	}
	return account, true
}

// accountsJoinCodesCreate is Accounts::JoinCodesController#create: `Current.account.reset_join_code`.
func (s *Server) accountsJoinCodesCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	account, ok := s.currentAccount(w, r)
	if !ok {
		return
	}
	if err := s.DB.AccountResetJoinCode(r.Context(), account.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteEditAccount())
}

// accountsCustomStylesEdit is Accounts::CustomStylesController#edit.
func (s *Server) accountsCustomStylesEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	account, ok := s.currentAccount(w, r)
	if !ok || !s.findTemplate(w, r) {
		return
	}
	var customStyles *string
	if account.HasCustomStyles {
		customStyles = &account.CustomStyles
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &customStylesEditSize, func(ctx *views.ViewContext) views.Page {
		return &views.AccountsCustomStylesEdit{Ctx: ctx, CustomStyles: customStyles}
	})
}

// accountsCustomStylesUpdate is Accounts::CustomStylesController#update:
// `@account.update!(params.require(:account).permit(:custom_styles))`.
func (s *Server) accountsCustomStylesUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	account, ok := s.currentAccount(w, r)
	if !ok || !requireParam(w, r, "account") {
		return
	}
	var customStyles *database.NullString
	if r.Form.Has("account[custom_styles]") {
		customStyles = &database.NullString{}
		if value := scalarParam(r, "account[custom_styles]"); value != nil {
			*customStyles = database.NullString{String: *value, Valid: true}
		}
	}
	if err := s.DB.AccountUpdate(r.Context(), account, nil, customStyles, nil); err != nil {
		s.fail(w, err)
		return
	}
	s.flash(r, "notice", "✓")
	s.redirectToPath(w, r, views.RouteEditAccountCustomStyles())
}

// --- Accounts::UsersController ------------------------------------------------------------------

// accountsUsersIndex is Accounts::UsersController#index: the people list's next pages, rendered
// as index.turbo_stream.erb (the only template, so other formats are 406).
func (s *Server) accountsUsersIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	if respondFormat(w, r, "turbo_stream") == "" {
		return
	}
	users, err := s.DB.UsersActiveOrderedWithoutBots(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	page := newGearedPage(r, int64(len(users)), accountsUsersPerPage)
	records := page.records(len(users))
	summaries := make([]views.UserSummary, 0, len(records))
	for _, i := range records {
		summaries = append(summaries, s.presentUserSummary(users[i]))
	}
	nextPage := page.nextPage()
	s.bare(w, r, &u, http.StatusOK, "turbo_stream", turboStreamContentType, func(ctx *views.ViewContext) *views.RecordedPage {
		return views.Render(0, func(qw *qt.Writer) { views.StreamAccountsUsersIndexTurboStream(qw, ctx, summaries, nextPage) })
	})
}

// accountsUsersUpdate is Accounts::UsersController#update:
// `@user.update(role: params.require(:user)[:role].presence_in(%w[ member administrator ]) || "member")`.
func (s *Server) accountsUsersUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	user, ok := s.setActiveUser(w, r)
	if !ok || !requireParam(w, r, "user") {
		return
	}
	role := 0
	if value := scalarParam(r, "user[role]"); value != nil && *value == "administrator" {
		role = 1
	}
	if err := s.DB.UserUpdateRole(r.Context(), user, role); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteEditAccount())
}

// accountsUsersDestroy is Accounts::UsersController#destroy: `@user.deactivate`.
func (s *Server) accountsUsersDestroy(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	user, ok := s.setActiveUser(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeactivateUser(r.Context(), user.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.Cable.Disconnect(user.ID)
	s.redirectToPath(w, r, views.RouteEditAccount())
}

// setActiveUser is `User.active.find(params[:user_id] || params[:id])`.
func (s *Server) setActiveUser(w http.ResponseWriter, r *http.Request) (database.User, bool) {
	value, found := paramValue(r, "user_id")
	if !found {
		value, found = paramValue(r, "id")
	}
	id, ok := integerCast(value)
	if !found || !ok {
		publicError(w, r, http.StatusNotFound)
		return database.User{}, false
	}
	user, found, err := s.DB.UserFindActive(r.Context(), id)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	if err != nil {
		s.fail(w, err)
		return user, false
	}
	return user, true
}

// --- Accounts::BotsController -------------------------------------------------------------------

// accountsBotsIndex is Accounts::BotsController#index: `@bots = User.active_bots.ordered`.
func (s *Server) accountsBotsIndex(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) || !s.findTemplate(w, r) {
		return
	}
	users, err := s.DB.UsersActiveBotsOrdered(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	bots := make([]views.Bot, 0, len(users))
	for _, bot := range users {
		view, err := s.presentBot(r, bot)
		if err != nil {
			s.fail(w, err)
			return
		}
		bots = append(bots, view)
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &botsIndexSize, func(ctx *views.ViewContext) views.Page {
		return &views.AccountsBotsIndex{Ctx: ctx, Bots: bots}
	})
}

// presentBot is a bot row for accounts/bots/_bot: its key and `bot.rooms.without_directs.ordered`.
func (s *Server) presentBot(r *http.Request, bot database.User) (views.Bot, error) {
	rooms, err := s.DB.RoomsForUserWithoutDirects(r.Context(), bot.ID)
	if err != nil {
		return views.Bot{}, err
	}
	// `ORDER BY LOWER(name)`: SQLite lowercases ASCII only.
	sort.SliceStable(rooms, func(i, j int) bool { return asciiLower(rooms[i].Name.String) < asciiLower(rooms[j].Name.String) })
	view := views.Bot{User: s.presentUserSummary(bot), BotKey: bot.BotKey(), Rooms: make([]views.BotRoom, len(rooms))}
	for i, room := range rooms {
		view.Rooms[i] = views.BotRoom{ID: room.ID, Name: room.Name.String}
	}
	return view, nil
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// accountsBotsNew is Accounts::BotsController#new.
func (s *Server) accountsBotsNew(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) || !s.findTemplate(w, r) {
		return
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &botsNewSize, func(ctx *views.ViewContext) views.Page {
		return &views.AccountsBotsNew{Ctx: ctx}
	})
}

// accountsBotsCreate is Accounts::BotsController#create: `User.create_bot! bot_params`.
func (s *Server) accountsBotsCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) || !requireParam(w, r, "user") {
		return
	}
	name := scalarParam(r, "user[name]")
	if name == nil {
		s.fail(w, errors.New("NOT NULL constraint failed: users.name"))
		return
	}
	// `create_webhook!(url: webhook_url) if webhook_url`: any non-nil value, "" included.
	webhookURL := scalarParam(r, "user[webhook_url]")
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.UserCreateBot(r.Context(), *name, webhookURL, recordAttachment(r, "user[avatar]", upload, false)); err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	s.redirectToPath(w, r, views.RouteAccountBots())
}

// accountsBotsEdit is Accounts::BotsController#edit.
func (s *Server) accountsBotsEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	bot, ok := s.findActiveBot(w, r, "id")
	if !ok || !s.findTemplate(w, r) {
		return
	}
	form, err := s.presentBotForm(r, bot)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &botsEditSize, func(ctx *views.ViewContext) views.Page {
		return &views.AccountsBotsEdit{Ctx: ctx, BotID: bot.ID, Bot: form}
	})
}

// presentBotForm is the fields accounts/bots/_form fills in: `image_tag bot.avatar` is the
// blob's absolute redirect URL.
func (s *Server) presentBotForm(r *http.Request, bot database.User) (views.BotForm, error) {
	avatar, attached, err := s.DB.AttachedBlob(r.Context(), "User", bot.ID, "avatar")
	if err != nil {
		return views.BotForm{}, err
	}
	webhookURL, err := s.DB.UserWebhookURL(r.Context(), bot.ID)
	if err != nil {
		return views.BotForm{}, err
	}
	form := views.BotForm{Name: views.Ptr(bot.Name), WebhookURL: webhookURL}
	if attached {
		form.AvatarAttachmentURL = views.Ptr(s.origin(r) + s.Storage.BlobURL(storage.Blob{ID: avatar.ID, Filename: avatar.Filename}))
	}
	return form, nil
}

// accountsBotsUpdate is Accounts::BotsController#update: `@bot.update_bot! bot_params`.
func (s *Server) accountsBotsUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	bot, ok := s.findActiveBot(w, r, "id")
	if !ok || !requireParam(w, r, "user") {
		return
	}
	name, webhookURL := scalarParam(r, "user[name]"), scalarParam(r, "user[webhook_url]")
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.UserUpdateBot(r.Context(), bot, name, webhookURL, recordAttachment(r, "user[avatar]", upload, false)); err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	s.redirectToPath(w, r, views.RouteAccountBots())
}

// accountsBotsDestroy is Accounts::BotsController#destroy: `@bot.deactivate`.
func (s *Server) accountsBotsDestroy(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	bot, ok := s.findActiveBot(w, r, "id")
	if !ok {
		return
	}
	if err := s.DB.DeactivateUser(r.Context(), bot.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.Cable.Disconnect(bot.ID)
	s.redirectToPath(w, r, views.RouteAccountBots())
}

// accountsBotsKeysUpdate is Accounts::Bots::KeysController#update:
// `User.active_bots.find(params[:bot_id]).reset_bot_key`.
func (s *Server) accountsBotsKeysUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !ensureCanAdminister(w, u) {
		return
	}
	bot, ok := s.findActiveBot(w, r, "bot_id")
	if !ok {
		return
	}
	if err := s.DB.UserResetBotKey(r.Context(), bot); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteAccountBots())
}

// findActiveBot is `User.active_bots.find(params[key])`.
func (s *Server) findActiveBot(w http.ResponseWriter, r *http.Request, key string) (database.User, bool) {
	value, _ := paramValue(r, key)
	id, ok := integerCast(value)
	if !ok {
		publicError(w, r, http.StatusNotFound)
		return database.User{}, false
	}
	bot, found, err := s.DB.UserFindActiveBot(r.Context(), id)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	if err != nil {
		s.fail(w, err)
		return bot, false
	}
	return bot, true
}

// --- Rooms::OpensController, Rooms::ClosedsController, Rooms::DirectsController ----------------

const defaultRoomName = "New room"

// roomsOpensNew is Rooms::OpensController#new.
func (s *Server) roomsOpensNew(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.ensurePermissionToCreateRooms(w, r, u) {
		return
	}
	users, err := s.activeUserViews(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	form := &views.OpenFormView{Room: views.FormRoom{Name: views.Ptr(defaultRoomName)}, CanAdminister: true, Users: users}
	s.pageOrFrame(w, r, &u, http.StatusOK, &opensNewSize, func(ctx *views.ViewContext) views.Page {
		return views.NewRoomsOpensNew(ctx, form)
	})
}

// roomsOpensCreate is Rooms::OpensController#create: `Rooms::Open.create_for(room_params, users:
// Current.user)`, prepended to everyone's shared rooms.
func (s *Server) roomsOpensCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.ensurePermissionToCreateRooms(w, r, u) {
		return
	}
	name, ok := roomNameParam(w, r)
	if !ok {
		return
	}
	room, err := s.DB.RoomCreateFor(r.Context(), "Rooms::Open", flattenName(name), u.ID, []int64{u.ID}, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	html, err := s.renderSharedRoom(r, room)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Cable.PublishStream(r.Context(), "rooms", stream("prepend", "shared_rooms", html))
	s.redirectToPath(w, r, views.RouteRoom(room.ID))
}

// roomsOpensEdit is Rooms::OpensController#edit.
func (s *Server) roomsOpensEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setFormRoom(w, r, u, false)
	if !ok {
		return
	}
	users, err := s.activeUserViews(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	form := &views.OpenFormView{Room: formRoom(room), CanAdminister: canAdministerRoom(u, room), Users: users}
	s.pageOrFrame(w, r, &u, http.StatusOK, &opensEditSize, func(ctx *views.ViewContext) views.Page {
		return views.NewRoomsOpensEdit(ctx, form)
	})
}

// roomsOpensUpdate is Rooms::OpensController#update: force_room_type, `@room.update! room_params`,
// then the room replaced in everyone's sidebar.
func (s *Server) roomsOpensUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setFormRoom(w, r, u, false)
	if !ok || !ensureCanAdministerRoom(w, u, room) {
		return
	}
	name, ok := roomNameParam(w, r)
	if !ok {
		return
	}
	room, err := s.DB.RoomUpdate(r.Context(), room, name, "Rooms::Open")
	if err != nil {
		s.fail(w, err)
		return
	}
	html, err := s.renderSharedRoom(r, room)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Cable.PublishStream(r.Context(), "rooms", stream("replace", views.RoomDomID(presentRoomKind(room.Type), room.ID, "list"), html))
	s.redirectToPath(w, r, views.RouteRoom(room.ID))
}

// roomsClosedsNew is Rooms::ClosedsController#new: `@users = User.active.ordered`, all unselected.
func (s *Server) roomsClosedsNew(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.ensurePermissionToCreateRooms(w, r, u) {
		return
	}
	users, err := s.activeUserViews(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	form := &views.ClosedFormView{Room: views.FormRoom{Name: views.Ptr(defaultRoomName)}, CanAdminister: true, CurrentUserID: u.ID, UnselectedUsers: users}
	s.pageOrFrame(w, r, &u, http.StatusOK, &closedsNewSize, func(ctx *views.ViewContext) views.Page {
		return views.NewRoomsClosedsNew(ctx, form)
	})
}

// roomsClosedsCreate is Rooms::ClosedsController#create: `Rooms::Closed.create_for(room_params,
// users: grantees)`, prepended to each member's shared rooms.
func (s *Server) roomsClosedsCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.ensurePermissionToCreateRooms(w, r, u) {
		return
	}
	name, ok := roomNameParam(w, r)
	if !ok {
		return
	}
	room, err := s.DB.RoomCreateFor(r.Context(), "Rooms::Closed", flattenName(name), u.ID, userIDsParam(r), true)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.broadcastClosedRoom(r, room, false); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteRoom(room.ID))
}

// roomsClosedsEdit is Rooms::ClosedsController#edit: the room's active members selected, the
// other active users not.
func (s *Server) roomsClosedsEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setFormRoom(w, r, u, false)
	if !ok {
		return
	}
	found, err := s.DB.RoomFind(r.Context(), room.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	selectedIDs, err := s.DB.RoomUserIDs(r.Context(), found.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	users, err := s.DB.UsersActiveOrdered(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	form := &views.ClosedFormView{Room: formRoom(room), CanAdminister: canAdministerRoom(u, room), CurrentUserID: u.ID}
	for _, user := range users {
		view := s.presentUserView(user)
		if containsID(selectedIDs, user.ID) {
			form.SelectedUsers = append(form.SelectedUsers, view)
		} else {
			form.UnselectedUsers = append(form.UnselectedUsers, view)
		}
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &closedsEditSize, func(ctx *views.ViewContext) views.Page {
		return views.NewRoomsClosedsEdit(ctx, form)
	})
}

// roomsClosedsUpdate is Rooms::ClosedsController#update: force_room_type, `@room.update!
// room_params`, `@room.memberships.revise(granted: grantees, revoked: revokees)`, then the room
// replaced in each remaining member's sidebar.
func (s *Server) roomsClosedsUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setFormRoom(w, r, u, false)
	if !ok || !ensureCanAdministerRoom(w, u, room) {
		return
	}
	name, ok := roomNameParam(w, r)
	if !ok {
		return
	}
	grantees := userIDsParam(r)
	room, err := s.DB.RoomUpdate(r.Context(), room, name, "Rooms::Closed")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.RoomRevise(r.Context(), room, grantees); err != nil {
		s.fail(w, err)
		return
	}
	if err = s.broadcastClosedRoom(r, room, true); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteRoom(room.ID))
}

// broadcastClosedRoom is broadcast_create_room / broadcast_update_room: the shared-room partial,
// rendered once, to every member's own rooms stream.
func (s *Server) broadcastClosedRoom(r *http.Request, room database.ReferenceRoom, update bool) error {
	html, err := s.renderSharedRoom(r, room)
	if err != nil {
		return err
	}
	members, err := s.DB.RoomUserIDs(r.Context(), room.ID)
	if err != nil {
		return err
	}
	action, target := "prepend", "shared_rooms"
	if update {
		action, target = "replace", views.RoomDomID(presentRoomKind(room.Type), room.ID, "list")
	}
	markup := stream(action, target, html)
	for _, user := range members {
		s.Cable.PublishStream(r.Context(), rails.UserRoomsStream(user), markup)
	}
	return nil
}

// roomFormHandlers are the room-type controllers' new, create, edit, update and destroy actions.
// Rooms::DirectsController has no update and the others' destroy raises: the route table
// answers those before they get here.
type roomFormHandlers struct {
	new, create, edit, update, destroy func(http.ResponseWriter, *http.Request, database.User)
}

func (s *Server) roomForms(namespace string) roomFormHandlers {
	switch namespace {
	case "closeds":
		return roomFormHandlers{s.roomsClosedsNew, s.roomsClosedsCreate, s.roomsClosedsEdit, s.roomsClosedsUpdate, s.deleteRoom}
	case "directs":
		return roomFormHandlers{s.roomsDirectsNew, s.roomsDirectsCreate, s.roomsDirectsEdit, s.saveRoom, s.roomsDirectsDestroy}
	}
	return roomFormHandlers{s.roomsOpensNew, s.roomsOpensCreate, s.roomsOpensEdit, s.roomsOpensUpdate, s.deleteRoom}
}

// roomsDirectsNew is Rooms::DirectsController#new.
func (s *Server) roomsDirectsNew(w http.ResponseWriter, r *http.Request, u database.User) {
	s.pageOrFrame(w, r, &u, http.StatusOK, &directsNewSize, func(ctx *views.ViewContext) views.Page {
		return &views.RoomsDirectsNew{Ctx: ctx}
	})
}

// roomsDirectsCreate is Rooms::DirectsController#create:
// `Rooms::Direct.find_or_create_for(selected_users)`, then each member's sidebar gets it.
func (s *Server) roomsDirectsCreate(w http.ResponseWriter, r *http.Request, u database.User) {
	// selected_users: `User.where(id: selected_users_ids.including(Current.user.id))`
	ids := append(userIDsParam(r), u.ID)
	room, err := s.DB.RoomFindOrCreateDirectFor(r.Context(), ids, u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.broadcastCreateDirectRoom(r, room); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteRoom(room.ID))
}

// broadcastCreateDirectRoom is broadcast_create_room: users/sidebars/rooms/_direct for each
// membership, to its user's rooms stream.
func (s *Server) broadcastCreateDirectRoom(r *http.Request, room database.ReferenceRoom) error {
	ctx, err := s.detachedViewContext(r.Context(), s.rendererBaseURL(r))
	if err != nil {
		return err
	}
	memberships, err := s.DB.MembershipsForRoom(r.Context(), room.ID)
	if err != nil {
		return err
	}
	partials := make(map[int64]string, len(memberships))
	for _, membership := range memberships {
		direct, err := s.presentSidebarDirect(r, membership)
		if err != nil {
			return err
		}
		partials[membership.ID] = views.CachedDirectRoom(ctx, &direct).HTML
	}
	memberships, err = s.DB.MembershipsForRoom(r.Context(), room.ID)
	if err != nil {
		return err
	}
	for _, membership := range memberships {
		s.Cable.PublishStream(r.Context(), rails.UserRoomsStream(membership.UserID), stream("prepend", "direct_rooms", partials[membership.ID]))
	}
	return nil
}

// presentSidebarDirect is users/sidebars/rooms/_direct's locals for membership:
// `room.users.without(membership.user).presence || [ membership.user ]`.
func (s *Server) presentSidebarDirect(r *http.Request, membership database.ReferenceMembership) (views.SidebarDirect, error) {
	room, err := s.DB.RoomFind(r.Context(), membership.RoomID)
	if err != nil {
		return views.SidebarDirect{}, err
	}
	users, err := s.DB.RoomUsers(r.Context(), room.ID)
	if err != nil {
		return views.SidebarDirect{}, err
	}
	var members []views.UserSummary
	for _, user := range users {
		if user.ID != membership.UserID {
			members = append(members, s.presentUserSummary(user))
		}
	}
	if len(members) == 0 {
		user, found, err := s.DB.UserFindByID(r.Context(), membership.UserID)
		if err == nil && !found {
			err = database.ErrNoRows
		}
		if err != nil {
			return views.SidebarDirect{}, err
		}
		members = append(members, s.presentUserSummary(user))
	}
	return views.SidebarDirect{RoomID: room.ID, Unread: membership.Unread(), UpdatedAtEpoch: strconv.FormatInt(views.EpochMS(room.UpdatedAt), 10),
		Members: members, MembershipID: membership.ID, MembershipUpdatedAt: membership.UpdatedAt}, nil
}

// roomsDirectsEdit is Rooms::DirectsController#edit: `@room.users.many? ?
// @room.users.without(Current.user) : @room.users`.
func (s *Server) roomsDirectsEdit(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setFormRoom(w, r, u, true)
	if !ok {
		return
	}
	users, err := s.DB.RoomUsers(r.Context(), room.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(users) > 1 {
		others := users[:0]
		for _, user := range users {
			if user.ID != u.ID {
				others = append(others, user)
			}
		}
		users = others
	}
	displayName, err := s.directRoomDisplayName(r, room, u)
	if err != nil {
		s.fail(w, err)
		return
	}
	edit := &views.DirectEditView{RoomID: room.ID, DisplayName: displayName, Users: make([]views.UserView, len(users))}
	for i, user := range users {
		edit.Users[i] = s.presentUserView(user)
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &directsEditSize, func(ctx *views.ViewContext) views.Page {
		return &views.RoomsDirectsEdit{Ctx: ctx, Edit: edit}
	})
}

// directRoomDisplayName is room_display_name(room, for_user:) for a direct room: its other
// members' names.
func (s *Server) directRoomDisplayName(r *http.Request, room database.ReferenceRoom, forUser database.User) (string, error) {
	users, err := s.DB.RoomUsers(r.Context(), room.ID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, user := range users {
		if user.ID != forUser.ID {
			names = append(names, user.Name)
		}
	}
	var name *string
	if room.Name.Valid {
		name = &room.Name.String
	}
	return views.RoomDisplayName(name, true, names, &forUser.Name), nil
}

// roomsDirectsDestroy is Rooms::DirectsController#destroy (RoomsController#destroy with its
// set_room; every member of a direct room can administer it).
func (s *Server) roomsDirectsDestroy(w http.ResponseWriter, r *http.Request, u database.User) {
	room, ok := s.setFormRoom(w, r, u, true)
	if !ok {
		return
	}
	if err := s.DB.DeleteRoom(r.Context(), room.ID); err != nil {
		s.fail(w, err)
		return
	}
	// broadcast_remove_to :rooms, target: [ @room, :list ]
	s.Cable.PublishStream(r.Context(), "rooms", stream("remove", views.RoomDomID(presentRoomKind(room.Type), room.ID, "list"), ""))
	s.redirectToPath(w, r, views.RouteRoot())
}

// setFormRoom is the room controllers' set_room: `Current.user.rooms.without_directs` (or
// `.directs`) `.find_by(id: params[:room_id] || params[:id])`, or back to the root with an alert.
func (s *Server) setFormRoom(w http.ResponseWriter, r *http.Request, u database.User, directs bool) (database.ReferenceRoom, bool) {
	value, found := paramValue(r, "room_id")
	if !found {
		value, found = paramValue(r, "id")
	}
	var room database.ReferenceRoom
	var inScope bool
	if id, ok := integerCast(value); found && ok {
		var err error
		room, inScope, err = s.DB.RoomForUser(r.Context(), u.ID, id)
		if err != nil {
			s.fail(w, err)
			return room, false
		}
	}
	if inScope && (room.Type == "Rooms::Direct") == directs {
		return room, true
	}
	s.flash(r, "alert", "Room not found or inaccessible")
	s.redirectToPath(w, r, views.RouteRoot())
	return room, false
}

// ensurePermissionToCreateRooms is `head :forbidden` when room creation is restricted to
// administrators and Current.user isn't one.
func (s *Server) ensurePermissionToCreateRooms(w http.ResponseWriter, r *http.Request, u database.User) bool {
	account, found, err := s.DB.AccountFirst(r.Context())
	if err != nil {
		s.fail(w, err)
		return false
	}
	if found && database.AccountSettingsRestrictRoomCreation(account.Settings) && u.Role != 1 {
		headStatus(w, http.StatusForbidden)
		return false
	}
	return true
}

// ensureCanAdministerRoom is `head :forbidden unless Current.user.can_administer?(@room)`.
func ensureCanAdministerRoom(w http.ResponseWriter, u database.User, room database.ReferenceRoom) bool {
	if !canAdministerRoom(u, room) {
		headStatus(w, http.StatusForbidden)
		return false
	}
	return true
}

// canAdministerRoom is `can_administer?(room)`: administrators and the room's creator.
func canAdministerRoom(u database.User, room database.ReferenceRoom) bool {
	return u.Role == 1 || room.CreatorID == u.ID
}

// formRoom is the persisted room the open and closed room forms edit.
func formRoom(room database.ReferenceRoom) views.FormRoom {
	form := views.FormRoom{ID: views.Ptr(room.ID)}
	if room.Name.Valid {
		form.Name = views.Ptr(room.Name.String)
	}
	return form
}

// activeUserViews is `User.active.ordered`.
func (s *Server) activeUserViews(r *http.Request) ([]views.UserView, error) {
	users, err := s.DB.UsersActiveOrdered(r.Context())
	if err != nil {
		return nil, err
	}
	result := make([]views.UserView, len(users))
	for i, user := range users {
		result[i] = s.presentUserView(user)
	}
	return result, nil
}

// renderSharedRoom renders users/sidebars/rooms/_shared for room outside the request
// (ApplicationController.render).
func (s *Server) renderSharedRoom(r *http.Request, room database.ReferenceRoom) (string, error) {
	sidebarRoom := views.SidebarRoom{ID: room.ID, ParamKey: presentRoomKind(room.Type).ParamKey(), Name: room.Name.String}
	if _, err := s.detachedViewContext(r.Context(), s.rendererBaseURL(r)); err != nil {
		return "", err
	}
	return views.RenderString(512, func(qw *qt.Writer) { views.StreamUsersSidebarsRoomsShared(qw, &sidebarRoom) }), nil
}

// roomNameParam is `params.require(:room).permit(:name)`: nil when no name was submitted, a NULL
// one for a non-string name.
func roomNameParam(w http.ResponseWriter, r *http.Request) (*database.NullString, bool) {
	if !requireParam(w, r, "room") {
		return nil, false
	}
	if !r.Form.Has("room[name]") {
		return nil, true
	}
	name := &database.NullString{}
	if value := scalarParam(r, "room[name]"); value != nil {
		*name = database.NullString{String: *value, Valid: true}
	}
	return name, true
}

func flattenName(name *database.NullString) *string {
	if name == nil || !name.Valid {
		return nil
	}
	return &name.String
}

// userIDsParam is `params.fetch(:user_ids, [])` as ids `User.where(id:)` can match.
func userIDsParam(r *http.Request) []int64 {
	var ids []int64
	if values, ok := r.Form["user_ids[]"]; ok {
		for _, value := range values {
			if id, ok := integerCast(value); ok {
				ids = append(ids, id)
			}
		}
	} else if value := scalarParam(r, "user_ids"); value != nil {
		if id, ok := integerCast(*value); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func containsID(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// --- Shared ---------------------------------------------------------------------------------------

// presentUserSummary is presenters::user_summary: a User row as the users views see it.
func (s *Server) presentUserSummary(u database.User) views.UserSummary {
	summary := views.UserSummary{ID: u.ID, Name: u.Name, Role: views.Role(u.Role), Status: views.Status(u.Status), AvatarPath: s.avatarPath(u.ID, u.UpdatedAt)}
	if u.Bio != "" {
		summary.Bio = views.Ptr(u.Bio)
	}
	if u.Email != "" {
		summary.EmailAddress = views.Ptr(u.Email)
	}
	return summary
}

// presentUserView is presenters::user_view.
func (s *Server) presentUserView(u database.User) views.UserView {
	return views.UserView{ID: u.ID, Name: u.Name, Title: u.Title(), AvatarURL: s.avatarPath(u.ID, u.UpdatedAt)}
}

// presentRoomKind is presenters::room_kind for a room's STI type.
func presentRoomKind(roomType string) views.RoomKind {
	switch roomType {
	case "Rooms::Closed":
		return views.RoomKindClosed
	case "Rooms::Direct":
		return views.RoomKindDirect
	}
	return views.RoomKindOpen
}

// ensureCanAdminister is ApplicationController's ensure_can_administer: `head :forbidden` unless
// Current.user is an administrator.
func ensureCanAdminister(w http.ResponseWriter, u database.User) bool {
	if u.Role != 1 {
		headStatus(w, http.StatusForbidden)
		return false
	}
	return true
}

// headStatus is `head status`: no body, and a bare text/html content type.
func headStatus(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status)
}

// redirectToPath is redirect_to(url_for(path)): a 302 to the absolute URL, with no body.
func (s *Server) redirectToPath(w http.ResponseWriter, r *http.Request, path string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Location", s.origin(r)+path)
	w.WriteHeader(http.StatusFound)
}

// requireParam is params.require(key): 400 unless the request has a non-blank key (a value or a
// hash of them).
func requireParam(w http.ResponseWriter, r *http.Request, key string) bool {
	prefix := key + "["
	for name, values := range r.Form {
		if name == key && len(values) > 0 && values[len(values)-1] != "" || strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if r.MultipartForm != nil {
		for name := range r.MultipartForm.File {
			if name == key || strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	publicError(w, r, http.StatusBadRequest)
	return false
}

// scalarParam is a permitted scalar's Param#to_s: nil when absent or null.
func scalarParam(r *http.Request, key string) *string {
	if !r.Form.Has(key) || nullParam(r, key) {
		return nil
	}
	return views.Ptr(r.Form.Get(key))
}

// paramValue is c.param_str(key): a path parameter, else a query or body one.
func paramValue(r *http.Request, key string) (string, bool) {
	if value := r.PathValue(key); value != "" {
		return value, true
	}
	if r.Form.Has(key) {
		return r.Form.Get(key), true
	}
	return "", false
}

// gearedPage is geared_pagination's page of a recordset with one per-page ratio
// (presenters/pagination.rs).
type gearedPage struct {
	number, count, perPage int64
}

// newGearedPage is `Recordset.new(records, per_page:).page(params[:page])`: `param.to_i > 0 ?
// param.to_i : 1`, capped so the arithmetic can't overflow.
func newGearedPage(r *http.Request, count, perPage int64) gearedPage {
	number := int64(0)
	if value, found := paramValue(r, "page"); found {
		if n, ok := rubyToI(value); ok {
			number = n
		} else if !strings.HasPrefix(strings.TrimLeft(value, " \t\n\v\f\r"), "-") {
			number = 1_000_000_000
		}
	}
	return gearedPage{number: min(max(number, 1), 1_000_000_000), count: count, perPage: perPage}
}

// pageCount is `recordset.page_count`.
func (p gearedPage) pageCount() int64 {
	return max((p.count+p.perPage-1)/p.perPage, 1)
}

// nextPage is `page.next_param` unless `page.last?`.
func (p gearedPage) nextPage() *string {
	if p.number == p.pageCount() {
		return nil
	}
	return views.Ptr(strconv.FormatInt(p.number+1, 10))
}

// records is the indexes of the page's slice of n ordered records.
func (p gearedPage) records(n int) []int {
	offset := (p.number - 1) * p.perPage
	var indexes []int
	for i := offset; i < offset+p.perPage && i < int64(n); i++ {
		indexes = append(indexes, int(i))
	}
	return indexes
}
