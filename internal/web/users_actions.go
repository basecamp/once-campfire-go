package web

import (
	"net/http"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/views"
	"golang.org/x/crypto/bcrypt"
)

// The users controllers' writes (reference/crates/campfire/src/controllers/users.rs,
// users/profiles.rs, users/bans.rs): their parameters, status codes, redirects and flash.

// paramsRequire is `params.require(key)` for a hash parameter: false (ParameterMissing, 400) when
// no key[...] parameter was given.
func paramsRequire(r *http.Request, key string) bool {
	prefix := key + "["
	for name := range r.Form {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// permittedString is a permitted string attribute given with a non-nil value (string_attribute
// flattened): key[field] present and not null.
func permittedString(r *http.Request, field string) (string, bool) {
	if !r.Form.Has(field) || nullParam(r, field) {
		return "", false
	}
	return r.Form.Get(field), true
}

// passwordDigest is has_secure_password's `password=`: nothing for a blank password.
func passwordDigest(password string, given bool) (*string, error) {
	if !given || password == "" {
		return nil, nil
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	value := string(digest)
	return &value, nil
}

// usersProfileUpdate is Users::ProfilesController#update: `@user.update user_params`, then
// `redirect_to user_profile_url, notice: update_notice`.
func (s *Server) usersProfileUpdate(w http.ResponseWriter, r *http.Request, u database.User) {
	// params.require(:user).permit(:name, :avatar, :email_address, :password, :bio).compact
	if !paramsRequire(r, "user") {
		publicError(w, r, http.StatusBadRequest)
		return
	}
	// user.update writes only what changed.
	changes := map[string]string{}
	if name, ok := permittedString(r, "user[name]"); ok && name != u.Name {
		changes["name"] = name
	}
	if email, ok := permittedString(r, "user[email_address]"); ok && (u.NullEmail || email != u.Email) {
		changes["email_address"] = email
	}
	password, given := permittedString(r, "user[password]")
	digest, err := passwordDigest(password, given)
	if err != nil {
		s.fail(w, err)
		return
	}
	if digest != nil {
		changes["password_digest"] = *digest
	}
	if bio, ok := permittedString(r, "user[bio]"); ok && (u.NullBio || bio != u.Bio) {
		changes["bio"] = bio
	}
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	avatar := recordAttachment(r, "user[avatar]", upload, true)
	// params[:user][:avatar] ? "It may take up to 30 minutes to change everywhere." : "✓"
	notice := "✓"
	if upload != nil || r.Form.Has("user[avatar]") && !nullParam(r, "user[avatar]") {
		notice = "It may take up to 30 minutes to change everywhere."
	}
	if len(changes) > 0 || avatar != nil {
		if err := s.DB.UpdateUser(r.Context(), u.ID, changes, nil, avatar); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.analyzeUpload(upload)
	s.flash(r, "notice", notice)
	s.redirectToPath(w, r, views.RouteUserProfile())
}

// usersBan is Users::BansController#create (ban) and #destroy (unban):
// `before_action :ensure_can_administer, :set_user`, then `redirect_to @user`.
func (s *Server) usersBan(ban bool) func(http.ResponseWriter, *http.Request, database.User) {
	return func(w http.ResponseWriter, r *http.Request, u database.User) {
		if u.Role != 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		user, ok := s.findUser(w, r, "user_id")
		if !ok {
			return
		}
		if err := s.DB.BanUser(r.Context(), user.ID, ban); err != nil {
			s.fail(w, err)
			return
		}
		if ban {
			// apply_ban's close_remote_connections
			s.Cable.Disconnect(user.ID)
		}
		s.redirectToPath(w, r, views.RouteUser(user.ID))
	}
}

// usersCreate is UsersController#create: `User.create!(user_params)` with the join code, then a
// new session; an email address already taken goes to the sign-in page instead.
func (s *Server) usersCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnauthenticatedAccess(w, r) {
		return
	}
	if _, ok := s.verifyJoinCode(w, r); !ok {
		return
	}
	// params.require(:user).permit(:name, :avatar, :email_address, :password)
	if !paramsRequire(r, "user") {
		publicError(w, r, http.StatusBadRequest)
		return
	}
	name, ok := permittedString(r, "user[name]")
	if !ok {
		// users.name is NOT NULL: a missing name fails the insert, as in Rails.
		publicError(w, r, http.StatusInternalServerError)
		return
	}
	var email *string
	if value, ok := permittedString(r, "user[email_address]"); ok {
		email = &value
	}
	password, given := permittedString(r, "user[password]")
	digest, err := passwordDigest(password, given)
	if err != nil {
		s.fail(w, err)
		return
	}
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	user, err := s.DB.CreateMember(r.Context(), name, email, digest, recordAttachment(r, "user[avatar]", upload, false))
	if database.IsRecordNotUnique(err) {
		// rescue ActiveRecord::RecordNotUnique: redirect_to new_session_url(email_address: user_params[:email_address])
		location := views.RouteNewSession()
		if email != nil {
			location += "?email_address=" + views.CGIEscape(*email)
		}
		s.redirectToPath(w, r, location)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	// start_new_session_for user
	token, err := s.DB.StartSession(r.Context(), user.ID, r.UserAgent(), remoteIP(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.setAuthenticationCookie(w, token); err != nil {
		s.fail(w, err)
		return
	}
	s.redirectToPath(w, r, views.RouteRoot())
}
