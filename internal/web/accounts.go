package web

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

// Account/profile effects are asynchronous in the reference. Preserve the
// committed redirect response while reporting processing/admission failures.
func logAccountProcessing(err error) {
	if err != nil {
		slog.Error("account post-commit processing failed", "error", err)
	}
}

func administrator(w http.ResponseWriter, u database.User) bool {
	if u.Role != 1 {
		http.Error(w, "Forbidden", 403)
		return false
	}
	return true
}

func accountPageNumber(raw string) int64 {
	var number int64
	fmt.Sscan(raw, &number)
	return number
}

func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, u database.User) {
	data, err := s.AccountQueries.Settings(r.Context(), u.Role, accountPageNumber(r.Form.Get("page")))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.respondPage(w, r, "account", 200, page{
		Title: "Account settings", User: u, NextPage: data.NextPage,
		Administrators: data.Administrators, AccountUsers: data.Users,
	})
}

func (s *Server) accountUsers(w http.ResponseWriter, r *http.Request, u database.User) {
	if respondFormat(w, r, "turbo_stream") == "" {
		return
	}
	data, err := s.AccountQueries.Members(r.Context(), accountPageNumber(r.Form.Get("page")))
	if err != nil {
		s.fail(w, err)
		return
	}
	body, err := s.Presentation.Markup("account-users-stream", page{
		User: u, AccountUsers: data.Users, NextPage: data.NextPage,
	})
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
	var restrict *sql.NullBool
	if r.Form.Has("account[settings][restrict_room_creation_to_administrators]") {
		value := r.Form.Get("account[settings][restrict_room_creation_to_administrators]")
		cast := sql.NullBool{Bool: true, Valid: true}
		// ActiveModel boolean casting is case-sensitive; blank remains JSON null.
		switch value {
		case "":
			cast = sql.NullBool{}
		case "0", "f", "F", "false", "FALSE", "off", "OFF":
			cast.Bool = false
		}
		restrict = &cast
	}
	upload, err := s.optionalUpload(r, "account[logo]")
	if err != nil {
		s.fail(w, err)
		return
	}
	logo, err := recordAttachment(r, "account[logo]", upload, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.AccountCommands.UpdateAccount(r.Context(), u.ID, database.AccountInput{Name: name, Restrict: restrict}, logo)
	if err != nil {
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
	s.flash(r, "notice", "✓")
	http.Redirect(w, r, "/account/edit", 302)
}

func (s *Server) resetJoinCode(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	if _, err := s.AccountCommands.UpdateAccount(r.Context(), u.ID, database.AccountInput{ResetJoin: true}, application.Attachment{}); err != nil {
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
		s.respondPage(w, r, "custom-styles", 200, page{Title: "Custom styles", User: u})
		return
	}
	if r.Form.Has("account[custom_styles]") {
		value := r.Form.Get("account[custom_styles]")
		if _, err := s.AccountCommands.UpdateAccount(r.Context(), u.ID, database.AccountInput{Styles: &value}, application.Attachment{}); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.flash(r, "notice", "✓")
	http.Redirect(w, r, "/account/custom_styles/edit", 302)
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request, u database.User) {
	if r.Method == "GET" || r.Method == "HEAD" {
		data, err := s.PageQueries.Profile(r.Context(), u.Participant())
		if err != nil {
			s.fail(w, err)
			return
		}
		p := page{
			Title: "My settings", User: u, Transfer: s.origin(r) + s.transferPath(u.ID),
			AvatarAttached: data.AvatarAttached, Memberships: data.Memberships,
			DirectMemberships: data.DirectMemberships,
		}
		s.respondPage(w, r, "profile", 200, p)
		return
	}
	attrs := database.UserChanges{}
	for _, field := range []struct {
		key    string
		target **string
	}{{"name", &attrs.Name}, {"email_address", &attrs.Email}, {"bio", &attrs.Bio}} {
		if r.Form.Has("user["+field.key+"]") && !nullParam(r, "user["+field.key+"]") {
			value := r.Form.Get("user[" + field.key + "]")
			*field.target = &value
		}
	}
	if password := r.Form.Get("user[password]"); password != "" {
		digest, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			http.Error(w, "Invalid password", 422)
			return
		}
		value := string(digest)
		attrs.Password = &value
	}
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	avatar, err := recordAttachment(r, "user[avatar]", upload, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.AccountCommands.UpdateUser(r.Context(), u.ID, u.ID, attrs, avatar)
	if err != nil {
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
	notice := "✓"
	if upload != nil || r.Form.Has("user[avatar]") && !nullParam(r, "user[avatar]") {
		notice = "It may take up to 30 minutes to change everywhere."
	}
	s.flash(r, "notice", notice)
	http.Redirect(w, r, "/users/me/profile", 302)
}

func (s *Server) showUser(w http.ResponseWriter, r *http.Request, u database.User) {
	subject, err := s.DB.UserProfile(r.Context(), pathInt(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.respondPage(
		w,
		r,
		"user",
		200,
		page{
			Title:    subject.Name,
			User:     u,
			Subject:  subject,
			Transfer: s.origin(r) + s.transferPath(subject.ID),
		},
	)
}

func (s *Server) manageUser(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	id := pathInt(r, "id")
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
		err = s.AccountCommands.Deactivate(r.Context(), u.ID, id)
	} else {
		role := 0
		if r.Form.Get("user[role]") == "administrator" {
			role = 1
		}
		_, err = s.AccountCommands.UpdateUser(r.Context(), u.ID, id, database.UserChanges{Role: &role}, application.Attachment{})
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
	id := pathInt(r, "user_id")
	if _, err := s.DB.User(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.AccountCommands.Ban(r.Context(), u.ID, id, r.Method == "POST")
	if err != nil {
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
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
	if account.JoinCode != r.PathValue("join_code") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == "GET" || r.Method == "HEAD" {
		s.respondPage(w, r, "join", 200, page{Title: "Join " + account.Name, JoinCode: account.JoinCode})
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
	result, err := s.AccountCommands.Join(r.Context(), r.PathValue("join_code"), remoteIP(r), database.UserInput{Name: r.Form.Get("user[name]"), Email: email, Password: string(digest)}, application.Attachment{File: upload})
	if err != nil {
		if existing, e := s.DB.UserByEmail(r.Context(), email); e == nil && existing.ID != 0 {
			http.Redirect(w, r, "/session/new?email_address="+url.QueryEscape(email), 302)
			return
		}
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
	s.startSession(w, r, result.Commit.User)
}

func (s *Server) bots(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	bots, err := s.BotQueries.Catalog(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	p := page{Title: "Bots", User: u, Bots: bots}
	s.respondPage(w, r, "bots", 200, p)
}

func (s *Server) botForm(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	data, err := s.BotQueries.Form(r.Context(), pathInt(r, "id"))
	if errors.Is(err, application.ErrNotBot) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	avatarURL := ""
	if data.Avatar != nil {
		avatarURL = storage.BlobURL(s.Storage.Verifier, *data.Avatar)
	}
	s.respondPage(
		w,
		r,
		"bot-form",
		200,
		page{AvatarURL: avatarURL, Title: "Bot settings", User: u, Bot: data.Bot, Webhook: data.Webhook},
	)
}

func (s *Server) saveBot(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	id := pathInt(r, "id")
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
		avatar, e := recordAttachment(r, "user[avatar]", upload, false)
		if e != nil {
			s.fail(w, e)
			return
		}
		result, e := s.AccountCommands.CreateUser(r.Context(), u.ID, database.UserInput{Name: name, Role: 2, Webhook: &webhook}, avatar)
		err = e
		logAccountProcessing(result.Processing)
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
			err = s.AccountCommands.DeactivateBot(r.Context(), u.ID, id)
		} else {
			avatar, e := recordAttachment(r, "user[avatar]", upload, false)
			if e != nil {
				s.fail(w, e)
				return
			}
			input := botChanges(r)
			input.Webhook = botWebhook(r)
			result, e := s.AccountCommands.UpdateBot(r.Context(), u.ID, id, input, avatar)
			err = e
			logAccountProcessing(result.Processing)
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/bots", 302)
}

func botChanges(r *http.Request) database.UserChanges {
	fields := database.UserChanges{}
	if r.Form.Has("user[name]") {
		value := r.Form.Get("user[name]")
		fields.Name = &value
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
	id := pathInt(r, "bot_id")
	bot, err := s.DB.User(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if bot.Role != 2 || bot.Status != 0 {
		http.NotFound(w, r)
		return
	}
	value := database.RandomToken(12)
	if _, err = s.AccountCommands.UpdateBot(r.Context(), u.ID, id, database.UserChanges{BotToken: &value}, application.Attachment{}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/bots", 302)
}

func (s *Server) transfer(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" || r.Method == "HEAD" {
		s.respondPage(w, r, "transfer", 200, page{Title: "Sign in", Transfer: r.PathValue("id")})
		return
	}
	id, err := s.Secrets.VerifyID("User", r.PathValue("id"), "transfer", s.DB.Now())
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
func (s *Server) transferPath(id int64) string {
	return "/session/transfers/" + s.Secrets.SignedID(
		"User",
		id,
		"transfer",
		s.DB.Now().Add(4*time.Hour),
	)
}
