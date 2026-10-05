package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) registerAccountRoutes() {
	s.mux.HandleFunc("GET /account/edit", s.auth(s.accountsEdit))
	s.mux.HandleFunc("PATCH /account", s.auth(s.accountsUpdate))
	s.mux.HandleFunc("PUT /account", s.auth(s.accountsUpdate))
	s.mux.HandleFunc("POST /account/join_code", s.auth(s.accountsJoinCodesCreate))
	s.mux.HandleFunc("GET /account/custom_styles/edit", s.auth(s.accountsCustomStylesEdit))
	s.mux.HandleFunc("PATCH /account/custom_styles", s.auth(s.accountsCustomStylesUpdate))
	s.mux.HandleFunc("PUT /account/custom_styles", s.auth(s.accountsCustomStylesUpdate))
	s.mux.HandleFunc("GET /users/{user_id}/profile", s.auth(s.usersProfileShow))
	s.mux.HandleFunc("PATCH /users/{user_id}/profile", s.auth(s.usersProfileUpdate))
	s.mux.HandleFunc("PUT /users/{user_id}/profile", s.auth(s.usersProfileUpdate))
	s.mux.HandleFunc("GET /users/{id}", s.auth(s.usersShow))
	s.mux.HandleFunc("POST /users/{user_id}/ban", s.auth(s.usersBan(true)))
	s.mux.HandleFunc("DELETE /users/{user_id}/ban", s.auth(s.usersBan(false)))
	s.mux.HandleFunc("PATCH /account/users/{user}", s.auth(s.accountsUsersUpdate))
	s.mux.HandleFunc("PUT /account/users/{user}", s.auth(s.accountsUsersUpdate))
	s.mux.HandleFunc("DELETE /account/users/{user}", s.auth(s.accountsUsersDestroy))
	s.mux.HandleFunc("GET /join/{join_code}", s.browserCheck(s.usersNew))
	s.mux.HandleFunc("POST /join/{join_code}", s.browserCheck(s.usersCreate))
	s.mux.HandleFunc("GET /account/users", s.auth(s.accountsUsersIndex))
	s.mux.HandleFunc("GET /account/bots", s.auth(s.accountsBotsIndex))
	s.mux.HandleFunc("GET /account/bots/new", s.auth(s.accountsBotsNew))
	s.mux.HandleFunc("GET /account/bots/{bot}/edit", s.auth(s.accountsBotsEdit))
	s.mux.HandleFunc("POST /account/bots", s.auth(s.accountsBotsCreate))
	s.mux.HandleFunc("PATCH /account/bots/{bot}", s.auth(s.accountsBotsUpdate))
	s.mux.HandleFunc("PUT /account/bots/{bot}", s.auth(s.accountsBotsUpdate))
	s.mux.HandleFunc("DELETE /account/bots/{bot}", s.auth(s.accountsBotsDestroy))
	s.mux.HandleFunc("PATCH /account/bots/{bot}/key", s.auth(s.accountsBotsKeysUpdate))
	s.mux.HandleFunc("PUT /account/bots/{bot}/key", s.auth(s.accountsBotsKeysUpdate))
	s.mux.HandleFunc("GET /session/transfers/{token}", s.browserCheck(s.transfer))
	s.mux.HandleFunc("PATCH /session/transfers/{token}", s.browserCheck(s.transfer))
	s.mux.HandleFunc("PUT /session/transfers/{token}", s.browserCheck(s.transfer))
}
func administrator(w http.ResponseWriter, u database.User) bool {
	if u.Role != 1 {
		http.Error(w, "Forbidden", 403)
		return false
	}
	return true
}
func accountPage(raw string, count int) (int64, int64) {
	var number int64
	fmt.Sscan(raw, &number)
	number = max(1, min(number, 1_000_000_000))
	last := int64(max(1, (count+499)/500))
	next := number + 1
	if number == last {
		next = 0
	}
	return number, next
}
func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, u database.User) {
	users, err := s.DB.AccountUsers(r.Context(), u.Role == 1)
	if err != nil {
		s.fail(w, err)
		return
	}
	_, next := accountPage(r.Form.Get("page"), len(users))
	p := page{Title: "Account settings", User: u, NextPage: next}
	for _, user := range users {
		if user.Role == 1 {
			p.Administrators = append(p.Administrators, user)
		} else {
			p.Users = append(p.Users, user)
		}
	}
	s.render(w, r, "account", 200, p)
}
func (s *Server) accountUsers(w http.ResponseWriter, r *http.Request, u database.User) {
	if respondFormat(w, r, "turbo_stream") == "" {
		return
	}
	users, err := s.DB.AccountUsers(r.Context(), false)
	if err != nil {
		s.fail(w, err)
		return
	}
	number, next := accountPage(r.Form.Get("page"), len(users))
	start := min(int((number-1)*500), len(users))
	body, err := s.markup("account-users-stream", page{User: u, Users: users[start:min(start+500, len(users))], NextPage: next})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeStream(w, body)
}
func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	var name *string
	if r.Form.Has("account[name]") {
		value := r.Form.Get("account[name]")
		name = &value
	}
	var restrict *bool
	if r.Form.Has("account[settings][restrict_room_creation_to_administrators]") {
		value := r.Form.Get("account[settings][restrict_room_creation_to_administrators]")
		on := value != "0" && value != "false" && value != ""
		restrict = &on
	}
	upload, err := s.optionalUpload(r, "account[logo]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.UpdateAccount(r.Context(), name, nil, restrict, false, recordAttachment(r, "account[logo]", upload, false)); err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	s.flash(r, "notice", "✓")
	http.Redirect(w, r, "/account/edit", 302)
}
func (s *Server) resetJoinCode(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	if err := s.DB.UpdateAccount(r.Context(), nil, nil, nil, true); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/edit", 302)
}
func (s *Server) customStyles(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	if r.Method == "GET" || r.Method == "HEAD" {
		s.render(w, r, "custom-styles", 200, page{Title: "Custom styles", User: u})
		return
	}
	value := r.Form.Get("account[custom_styles]")
	if err := s.DB.UpdateAccount(r.Context(), nil, &value, nil, false); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/edit", 302)
}
func (s *Server) profile(w http.ResponseWriter, r *http.Request, u database.User) {
	if r.Method == "GET" || r.Method == "HEAD" {
		rooms, err := s.DB.AllRooms(r.Context(), u.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		p := page{Title: "My settings", User: u, Rooms: rooms, Transfer: s.origin(r) + s.transferPath(u)}
		_, err = s.Storage.Attached(r.Context(), "User", u.ID, "avatar")
		p.AvatarAttached = err == nil
		if err != nil && !errors.Is(err, database.ErrNoRows) {
			s.fail(w, err)
			return
		}
		for _, room := range rooms {
			involvement, err := s.DB.Involvement(r.Context(), u.ID, room.ID)
			if err != nil {
				s.fail(w, err)
				return
			}
			view, err := s.displayRoom(r.Context(), room, u)
			if err != nil {
				s.fail(w, err)
				return
			}
			if room.Type == "Rooms::Direct" {
				p.DirectMemberships = append(p.DirectMemberships, profileMembership{view.Room, involvement})
			} else {
				p.Memberships = append(p.Memberships, profileMembership{view.Room, involvement})
			}
		}
		s.render(w, r, "profile", 200, p)
		return
	}
	attrs := map[string]string{}
	for _, key := range []string{"name", "email_address", "bio"} {
		if r.Form.Has("user["+key+"]") && !nullParam(r, "user["+key+"]") {
			attrs[key] = r.Form.Get("user[" + key + "]")
		}
	}
	if password := r.Form.Get("user[password]"); password != "" {
		digest, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			http.Error(w, "Invalid password", 422)
			return
		}
		attrs["password_digest"] = string(digest)
	}
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.DB.UpdateUser(r.Context(), u.ID, attrs, nil, recordAttachment(r, "user[avatar]", upload, true)); err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	notice := "✓"
	if upload != nil || r.Form.Has("user[avatar]") && !nullParam(r, "user[avatar]") {
		notice = "It may take up to 30 minutes to change everywhere."
	}
	s.flash(r, "notice", notice)
	http.Redirect(w, r, "/users/me/profile", 302)
}
func (s *Server) showUser(w http.ResponseWriter, r *http.Request, u database.User) {
	subject, err := s.DB.User(r.Context(), pathInt(r, "user"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "user", 200, page{Title: subject.Name, User: u, Subject: subject, Transfer: s.origin(r) + s.transferPath(subject)})
}
func (s *Server) manageUser(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	id := pathInt(r, "user")
	subject, err := s.DB.User(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if subject.Status != 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method == "DELETE" {
		err = s.DB.DeactivateUser(r.Context(), id)
		s.Cable.Disconnect(id)
	} else {
		role := "0"
		if r.Form.Get("user[role]") == "administrator" {
			role = "1"
		}
		err = s.DB.UpdateUser(r.Context(), id, map[string]string{"role": role}, nil)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/edit", 302)
}
func (s *Server) banUser(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	id := pathInt(r, "user")
	if _, err := s.DB.User(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.DB.BanUser(r.Context(), id, r.Method == "POST"); err != nil {
		s.fail(w, err)
		return
	}
	s.Cable.Disconnect(id)
	http.Redirect(w, r, fmt.Sprintf("/users/%d", id), 302)
}
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnauthenticated(w, r) {
		return
	}
	account, err := s.DB.Account(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if account.JoinCode != r.PathValue("code") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == "GET" || r.Method == "HEAD" {
		s.render(w, r, "join", 200, page{Title: "Join " + account.Name, JoinCode: account.JoinCode})
		return
	}
	banned, err := s.DB.BannedIP(r.Context(), remoteIP(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if banned {
		http.Error(w, "Forbidden", 403)
		return
	}
	password := r.Form.Get("user[password]")
	if password == "" {
		http.Error(w, "Password is required", 422)
		return
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		http.Error(w, "Invalid password", 422)
		return
	}
	email := r.Form.Get("user[email_address]")
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	user, err := s.DB.CreateUser(r.Context(), r.Form.Get("user[name]"), email, string(digest), "", 0, nil, pendingBlob(upload))
	if err != nil {
		if existing, e := s.DB.UserByEmail(r.Context(), email); e == nil && existing.ID != 0 {
			http.Redirect(w, r, "/session/new?email_address="+url.QueryEscape(email), 302)
			return
		}
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	s.startSession(w, r, user)
}
func (s *Server) bots(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	bots, err := s.DB.Users(r.Context(), 0, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	p := page{Title: "Bots", User: u, Users: bots}
	for _, bot := range bots {
		rooms, err := s.DB.AllRooms(r.Context(), bot.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		for i, room := range rooms {
			view, err := s.displayRoom(r.Context(), room, bot)
			if err != nil {
				s.fail(w, err)
				return
			}
			rooms[i] = view.Room
		}
		shared := rooms[:0]
		for _, room := range rooms {
			if room.Type != "Rooms::Direct" {
				shared = append(shared, room)
			}
		}
		p.Bots = append(p.Bots, botView{bot, shared})
	}
	s.render(w, r, "bots", 200, p)
}
func (s *Server) botForm(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	bot := database.User{Role: 2}
	webhook := ""
	if id := pathInt(r, "bot"); id != 0 {
		var err error
		bot, err = s.DB.User(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		if bot.Role != 2 || bot.Status != 0 {
			http.NotFound(w, r)
			return
		}
		err = s.DB.Read.QueryRowContext(r.Context(), "SELECT url FROM webhooks WHERE user_id=?", id).Scan(&webhook)
		if err != nil && !errors.Is(err, database.ErrNoRows) {
			s.fail(w, err)
			return
		}
	}
	avatarURL := ""
	if bot.ID != 0 {
		blob, e := s.Storage.Attached(r.Context(), "User", bot.ID, "avatar")
		if e == nil {
			avatarURL = s.Storage.BlobURL(blob)
		} else if !errors.Is(e, database.ErrNoRows) {
			s.fail(w, e)
			return
		}
	}
	s.render(w, r, "bot-form", 200, page{AvatarURL: avatarURL, Title: "Bot settings", User: u, Subject: bot, Webhook: webhook})
}
func (s *Server) saveBot(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	id := pathInt(r, "bot")
	webhook := r.Form.Get("user[webhook_url]")
	name := r.Form.Get("user[name]")
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if upload != nil {
		defer upload.Discard()
	}
	if id == 0 {
		_, err = s.DB.CreateUser(r.Context(), name, "", "", "", 2, &webhook, recordAttachment(r, "user[avatar]", upload, false))
	} else {
		bot, e := s.DB.User(r.Context(), id)
		if e != nil {
			s.fail(w, e)
			return
		}
		if bot.Role != 2 || bot.Status != 0 {
			http.NotFound(w, r)
			return
		}
		if r.Method == "DELETE" {
			err = s.DB.DeactivateUser(r.Context(), id)
			s.Cable.Disconnect(id)
		} else {
			err = s.DB.UpdateUser(r.Context(), id, botChanges(r), botWebhook(r), recordAttachment(r, "user[avatar]", upload, false))
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Method != "DELETE" {
		s.analyzeUpload(upload)
	}
	http.Redirect(w, r, "/account/bots", 302)
}
func botChanges(r *http.Request) map[string]string {
	fields := map[string]string{}
	if r.Form.Has("user[name]") {
		fields["name"] = r.Form.Get("user[name]")
	}
	return fields
}
func botWebhook(r *http.Request) *string {
	if !r.Form.Has("user[webhook_url]") {
		return nil
	}
	value := r.Form.Get("user[webhook_url]")
	return &value
}
func (s *Server) rotateBot(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	id := pathInt(r, "bot")
	bot, err := s.DB.User(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if bot.Role != 2 || bot.Status != 0 {
		http.NotFound(w, r)
		return
	}
	if err = s.DB.UpdateUser(r.Context(), id, map[string]string{"bot_token": database.RandomToken(12)}, nil); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/bots", 302)
}
func (s *Server) transfer(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" || r.Method == "HEAD" {
		s.render(w, r, "transfer", 200, page{Title: "Sign in", Transfer: r.PathValue("token")})
		return
	}
	id, err := s.Secrets.VerifyID("User", r.PathValue("token"), "transfer", s.DB.Now())
	if err != nil {
		http.Error(w, "Bad request", 400)
		return
	}
	u, err := s.DB.User(r.Context(), id)
	if err != nil || u.Status != 0 {
		http.Error(w, "Bad request", 400)
		return
	}
	s.startSession(w, r, u)
}

// Transfer links expire after the same four-hour window as User#transfer_id.
func (s *Server) transferPath(u database.User) string {
	return "/session/transfers/" + s.Secrets.SignedID("User", u.ID, "transfer", s.DB.Now().Add(4*time.Hour))
}
