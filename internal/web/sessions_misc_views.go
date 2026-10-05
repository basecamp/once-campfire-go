package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"github.com/basecamp/once-campfire-go/internal/qrcode"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/views"
	"golang.org/x/crypto/bcrypt"
)

// Sign in and out, session transfers, first run, the root URL, the PWA files, avatars and QR
// codes, as the reference's controllers serve them (reference/crates/campfire/src/controllers:
// sessions.rs, sessions/transfers.rs, first_runs.rs, welcome.rs, rooms.rs#index, pwa.rs,
// users/avatars.rs, qr_code.rs), and AllowBrowser's incompatible-browser page (concerns.rs).

var (
	sessionsNewSize, sessionsTransfersShowSize, firstRunsShowSize, welcomeShowSize views.RenderSize
	incompatibleBrowserSize                                                        views.RenderSize
)

// sessionsRejection is SessionsController::REJECTION, the alert of a failed or rate-limited sign in.
const sessionsRejection = "Too many requests or unauthorized."

// sessionsNew is SessionsController#new (allow_unauthenticated_access): ensure_user_exists, then
// the sign-in page.
func (s *Server) sessionsNew(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.UserCount(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if users == 0 {
		s.redirectTo(w, r, s.origin(r)+views.RouteFirstRun())
		return
	}
	s.renderSessionsNew(w, r, http.StatusOK, false)
}

// sessionsCreate is SessionsController#create: rate_limit, then User.active.authenticate_by.
func (s *Server) sessionsCreate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsRateLimit(r) {
		s.renderSessionsNew(w, r, http.StatusTooManyRequests, true)
		return
	}
	email, hasEmail := paramString(r, "email_address")
	password, hasPassword := paramString(r, "password")
	var user database.User
	authenticated := false
	if hasEmail && hasPassword {
		var err error
		user, authenticated, err = s.authenticateBy(r, email, password)
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if !authenticated {
		s.renderSessionsNew(w, r, http.StatusUnauthorized, true)
		return
	}
	if !s.startNewSessionFor(w, r, user) {
		return
	}
	s.redirectTo(w, r, s.postAuthenticationURL(r))
}

// sessionsDestroy is SessionsController#destroy: remove_push_subscription, then
// terminate_current_session.
func (s *Server) sessionsDestroy(w http.ResponseWriter, r *http.Request, u database.User) {
	if endpoint, ok := paramString(r, "push_subscription_endpoint"); ok {
		if err := s.DB.PushSubscriptionDestroyByEndpoint(r.Context(), u.ID, endpoint); err != nil {
			s.fail(w, err)
			return
		}
	}
	session, found, err := s.currentSession(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if found {
		if err := s.DB.SessionDestroy(r.Context(), session.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	browserState(r).reset()
	w.Header().Add("Set-Cookie", "session_token=; path=/; max-age=0; expires=Thu, 01 Jan 1970 00:00:00 GMT; samesite=lax")
	s.Cable.Reconnect(u.ID)
	s.redirectTo(w, r, s.origin(r)+views.RouteRoot())
}

// renderSessionsNew is render_new, or with rejected render_rejection's `flash.now[:alert]` first.
func (s *Server) renderSessionsNew(w http.ResponseWriter, r *http.Request, status int, rejected bool) {
	if !s.findTemplate(w, r) {
		return
	}
	var email *string
	if value, ok := paramString(r, "email_address"); ok {
		email = &value
	}
	helpContact, err := s.helpContact(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.pageOrFrame(w, r, nil, status, &sessionsNewSize, func(ctx *views.ViewContext) views.Page {
		if rejected {
			ctx.FlashAlert = views.Ptr(sessionsRejection)
		}
		return &views.SessionsNew{Ctx: ctx, EmailAddress: email, HelpContact: helpContact}
	})
}

// helpContact is presenters::accounts::help_contact.
func (s *Server) helpContact(r *http.Request) (*views.HelpContact, error) {
	name, email, found, err := s.DB.HelpContact(r.Context())
	if err != nil || !found {
		return nil, err
	}
	return &views.HelpContact{Name: name, EmailAddress: email}, nil
}

// sessionsRateLimit is `rate_limit to: 10, within: 3.minutes, only: :create`: a counter per
// remote IP whose window starts with its first request and doesn't slide.
func (s *Server) sessionsRateLimit(r *http.Request) bool {
	const to, within = 10, 3 * time.Minute
	key := remoteIP(r)
	now := s.DB.Now()
	s.attemptsMu.Lock()
	defer s.attemptsMu.Unlock()
	for k, a := range s.attempts {
		if !a.Start.Add(within).After(now) {
			delete(s.attempts, k)
		}
	}
	a, ok := s.attempts[key]
	if !ok {
		a.Start = now
	}
	a.Count++
	s.attempts[key] = a
	return a.Count <= to
}

// authenticateBy is `User.active.authenticate_by(email_address:, password:)`: nil for a blank
// password before any lookup, and a missing user still pays for a bcrypt comparison.
func (s *Server) authenticateBy(r *http.Request, email, password string) (database.User, bool, error) {
	if password == "" {
		return database.User{}, false, nil
	}
	user, found, err := s.DB.UserFindActiveByEmailAddress(r.Context(), email)
	if err != nil {
		return database.User{}, false, err
	}
	digest := s.dummyHash
	if found {
		if user.Password == "" {
			return database.User{}, false, nil
		}
		digest = []byte(user.Password)
	}
	// bcrypt reads at most 72 bytes of a password, as BCrypt and the reference's bcrypt::verify do.
	secret := []byte(password)
	if len(secret) > 72 {
		secret = secret[:72]
	}
	valid := bcrypt.CompareHashAndPassword(digest, secret) == nil
	return user, found && valid, nil
}

// startNewSessionFor is start_new_session_for(user): a new session and its permanent signed
// session_token cookie.
func (s *Server) startNewSessionFor(w http.ResponseWriter, r *http.Request, user database.User) bool {
	agent, ip := r.UserAgent(), remoteIP(r)
	var userAgent *string
	if _, ok := r.Header["User-Agent"]; ok {
		userAgent = &agent
	}
	token, err := s.DB.SessionStart(r.Context(), user.ID, userAgent, &ip)
	if err == nil {
		err = s.setAuthenticationCookie(w, token)
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	return true
}

// currentSession is Current.session: the session the session_token cookie names.
func (s *Server) currentSession(r *http.Request) (database.ReferenceSession, bool, error) {
	cookie, err := r.Cookie("session_token")
	if err != nil {
		return database.ReferenceSession{}, false, nil
	}
	var token string
	if s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), s.DB.Now(), &token) != nil || token == "" {
		return database.ReferenceSession{}, false, nil
	}
	return s.DB.SessionByToken(r.Context(), token)
}

// sessionTransfersShow is Sessions::TransfersController#show: a form that PUTs back to this URL.
func (s *Server) sessionTransfersShow(w http.ResponseWriter, r *http.Request) {
	action := r.URL.EscapedPath()
	if format := r.PathValue("format"); format != "" {
		action += "." + url.PathEscape(format)
	}
	s.pageOrFrame(w, r, nil, http.StatusOK, &sessionsTransfersShowSize, func(ctx *views.ViewContext) views.Page {
		return &views.SessionsTransfersShow{Ctx: ctx, Action: action}
	})
}

// sessionTransfersUpdate is Sessions::TransfersController#update: User.active.find_by_transfer_id,
// signed in, or 400.
func (s *Server) sessionTransfersUpdate(w http.ResponseWriter, r *http.Request) {
	var user database.User
	found := false
	if id, err := s.Secrets.VerifyID("User", r.PathValue("id"), "transfer", s.DB.Now()); err == nil {
		user, found, err = s.DB.UserFindByID(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		found = found && user.Status == 0
	}
	if !found {
		headStatus(w, r, http.StatusBadRequest)
		return
	}
	if !s.startNewSessionFor(w, r, user) {
		return
	}
	s.redirectTo(w, r, s.postAuthenticationURL(r))
}

// firstRunsShow is FirstRunsController#show (allow_unauthenticated_access, prevent_repeats).
func (s *Server) firstRunsShow(w http.ResponseWriter, r *http.Request) {
	if s.firstRunRepeated(w, r) {
		return
	}
	s.pageOrFrame(w, r, nil, http.StatusOK, &firstRunsShowSize, func(ctx *views.ViewContext) views.Page {
		return &views.FirstRunsShow{Ctx: ctx}
	})
}

// firstRunsCreate is FirstRunsController#create: FirstRun.create!(user_params), signed in as the
// administrator; a second first run (RecordNotUnique) goes to the root.
func (s *Server) firstRunsCreate(w http.ResponseWriter, r *http.Request) {
	if s.firstRunRepeated(w, r) {
		return
	}
	if !hasNestedParams(r, "user") {
		publicError(w, r, http.StatusBadRequest)
		return
	}
	name, hasName := paramString(r, "user[name]")
	email, _ := paramString(r, "user[email_address]")
	password, _ := paramString(r, "user[password]")
	if !hasName {
		s.fail(w, errors.New("NOT NULL constraint failed: users.name"))
		return
	}
	upload, err := s.optionalUpload(r, "user[avatar]")
	if err != nil {
		s.fail(w, err)
		return
	}
	if upload != nil {
		defer upload.Discard()
	}
	digest, err := bcrypt.GenerateFromPassword(truncatedPassword(password), 12)
	if err != nil {
		s.fail(w, err)
		return
	}
	administrator, err := s.DB.FirstRunCreate(r.Context(), name, email, string(digest), recordAttachment(r, "user[avatar]", upload, false))
	if database.IsRecordNotUnique(err) {
		s.redirectTo(w, r, s.origin(r)+views.RouteRoot())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	if !s.startNewSessionFor(w, r, administrator) {
		return
	}
	s.redirectTo(w, r, s.origin(r)+views.RouteRoot())
}

// firstRunRepeated is prevent_repeats: `redirect_to root_url if Account.any?`.
func (s *Server) firstRunRepeated(w http.ResponseWriter, r *http.Request) bool {
	accounts, err := s.DB.AccountCount(r.Context())
	if err != nil {
		s.fail(w, err)
		return true
	}
	if accounts > 0 {
		s.redirectTo(w, r, s.origin(r)+views.RouteRoot())
		return true
	}
	return false
}

// welcomeShow is WelcomeController#show: to the last room visited, or the no-rooms page.
func (s *Server) welcomeShow(w http.ResponseWriter, r *http.Request, u database.User) {
	rooms, err := s.DB.UserHasRooms(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rooms {
		room, found, err := s.lastRoomVisitedIn(r.Context(), u.ID, lastRoomCookie(r))
		if err == nil && !found {
			err = errors.New("no last room")
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		s.redirectTo(w, r, s.origin(r)+views.RouteRoom(room.ID))
		return
	}
	s.pageOrFrame(w, r, &u, http.StatusOK, &welcomeShowSize, func(ctx *views.ViewContext) views.Page {
		return &views.WelcomeShow{Ctx: ctx, CurrentUserName: u.Name}
	})
}

// roomsIndexView is RoomsController#index: to the user's newest room.
func (s *Server) roomsIndexView(w http.ResponseWriter, r *http.Request, u database.User) {
	room, found, err := s.DB.LastRoomForUser(r.Context(), u.ID)
	if err == nil && !found {
		err = errors.New("No route matches room_url(nil)")
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.redirectTo(w, r, s.origin(r)+views.RouteRoom(room.ID))
}

// pwaServiceWorker is PwaController#service_worker.
func (s *Server) pwaServiceWorker(w http.ResponseWriter, r *http.Request) {
	if s.respondTo(w, r, "js") == "" {
		return
	}
	s.renderAs(w, r, http.StatusOK, "text/javascript; charset=utf-8", views.ServiceWorkerJS)
}

// pwaManifest is PwaController#manifest.
func (s *Server) pwaManifest(w http.ResponseWriter, r *http.Request) {
	if s.respondTo(w, r, "json") == "" {
		return
	}
	account, found, err := s.DB.AccountFirst(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	var name, v *string
	if found {
		name, v = &account.Name, views.Ptr(toFSNumber(account.UpdatedAt))
	}
	body := views.PwaManifestJson(name, views.RouteFreshAccountLogo(v, views.Ptr("small")), views.RouteFreshAccountLogo(v, nil), s.origin(r), assets.Path)
	s.renderAs(w, r, http.StatusOK, "application/json; charset=utf-8", body)
}

// qrSlots bounds the QR codes being encoded at once.
var qrSlots = make(chan struct{}, 4)

// qrCodeShow is QrCodeController#show: an SVG QR code of a Base64url-encoded URL, cached a year.
func (s *Server) qrCodeShow(w http.ResponseWriter, r *http.Request) {
	value := strings.NewReplacer("-", "+", "_", "/").Replace(r.PathValue("id"))
	var data []byte
	var err error
	if strings.Contains(value, "=") {
		data, err = base64.StdEncoding.Strict().DecodeString(value)
	} else {
		data, err = base64.RawStdEncoding.Strict().DecodeString(value)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	select {
	case qrSlots <- struct{}{}:
		defer func() { <-qrSlots }()
	case <-r.Context().Done():
		return
	}
	body, ok := qrcode.SVG(data)
	if !ok {
		publicError(w, r, http.StatusUnprocessableEntity)
		return
	}
	s.expiresIn(w, 31556952, "public")
	s.renderAs(w, r, http.StatusOK, "image/svg+xml; charset=utf-8", body)
}

// avatarTemplateDigest is users/avatars/show.svg's template digest, which EtagWithTemplateDigest
// adds to the ETag when the template can be found for the request's formats.
const avatarTemplateDigest = "d500db55e2a67222018ef0156839c3c9"

// liveResponse is a controller that includes ActionController::Live (ActiveStorage::Streaming
// does): its responses skip config.action_dispatch.default_headers.
func liveResponse(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for _, key := range []string{"X-Frame-Options", "X-XSS-Protection", "X-Content-Type-Options", "X-Permitted-Cross-Domain-Policies", "Referrer-Policy"} {
			h.Del(key)
		}
		next(w, r)
	}
}

// avatarShow is Users::AvatarsController#show: the user's avatar by its signed token, fresh_when
// the user, then the uploaded image's :square variant, the default bot avatar, or the initials.
func (s *Server) avatarShow(w http.ResponseWriter, r *http.Request, _ database.User) {
	id, err := s.Secrets.VerifyID("User", r.PathValue("user_id"), "avatar", s.DB.Now())
	if err != nil {
		headStatus(w, r, http.StatusNotFound)
		return
	}
	user, found, err := s.DB.UserFindByID(r.Context(), id)
	if err == nil && !found {
		err = database.ErrNoRows
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	key := views.AppendCacheKeyWithVersion(make([]byte, 0, 64), "users", user.ID, user.UpdatedAt)
	if avatarTemplateFound(r) {
		key = append(append(key, '/'), avatarTemplateDigest...)
	}
	if s.freshWhen(w, r, key) {
		return
	}
	s.expiresIn(w, 30*60, "public", "stale-while-revalidate=604800")

	variant, found, err := s.avatarVariant(r, user.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	switch {
	case found:
		path, err := s.Storage.Path(variant.Key)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.sendFile(w, r, path, "image/webp")
	case user.Role == 2:
		data, err := fs.ReadFile(assets.Public(), strings.TrimPrefix(assets.Path("default-bot-avatar.svg"), "/"))
		if err != nil {
			s.fail(w, err)
			return
		}
		sendData(w, data, "image/svg+xml", "default-bot-avatar.svg")
	default:
		s.renderAs(w, r, http.StatusOK, "image/svg+xml; charset=utf-8", views.UsersAvatarsShowSvg(user.ID, views.Initials(user.Name)))
	}
}

// avatarTemplateFound is whether show.svg.erb is found for the request's formats: `*/*` or svg,
// or no registered format at all.
func avatarTemplateFound(r *http.Request) bool {
	formats, err := httpcompat.Formats(formatInput(r))
	if err != nil {
		return false
	}
	if len(formats) == 0 {
		return true
	}
	for _, format := range formats {
		if format == "*/*" || format == "svg" {
			return true
		}
	}
	return false
}

// avatarVariant is `avatar.variant(:square).processed if avatar.variable?`
// (`resize_to_limit: [512, 512], format: :webp`): the existing variant, or one processed now.
func (s *Server) avatarVariant(r *http.Request, user int64) (database.Blob, bool, error) {
	blob, found, err := s.DB.AttachedBlob(r.Context(), "User", user, "avatar")
	if err != nil || !found || !storage.Variable(blob.ContentType.String) {
		return database.Blob{}, false, err
	}
	source := storage.Blob{ID: blob.ID, Key: blob.Key, Filename: blob.Filename, Metadata: []byte(blob.Metadata.String),
		ServiceName: blob.ServiceName, ByteSize: blob.ByteSize, Checksum: blob.Checksum.String, CreatedAt: blob.CreatedAt}
	if blob.ContentType.Valid {
		source.ContentType = &blob.ContentType.String
	}
	variation := storage.Resize(512, 512, "webp")
	image, found, err := s.DB.ExistingVariant(r.Context(), blob.ID, storage.VariantDigest(source, variation))
	if err != nil || found {
		return image, found, err
	}
	processed, err := s.Storage.Variant(r.Context(), source, variation)
	if err != nil {
		return database.Blob{}, false, err
	}
	return database.Blob{ID: processed.ID, Key: processed.Key, Filename: processed.Filename}, true, nil
}

// freshWhen is fresh_when(etag: key): the weak ETag of the key with turbo-rails' frame etagger and
// the flash, and 304 when the request already has it.
func (s *Server) freshWhen(w http.ResponseWriter, r *http.Request, key []byte) bool {
	if strings.TrimSpace(r.Header.Get("Turbo-Frame")) != "" {
		key = append(key, "/frame"...)
	}
	if notice, alert := s.consumeFlash(r); notice != "" || alert != "" {
		var flashes []string
		if alert != "" {
			flashes = append(flashes, "alert=Some(String("+strconv.Quote(alert)+"))")
		}
		if notice != "" {
			flashes = append(flashes, "notice=Some(String("+strconv.Quote(notice)+"))")
		}
		key = append(append(key, '/'), strings.Join(flashes, "&")...)
	}
	sum := sha256.Sum256(key)
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	// Until expires_in says otherwise, a response with a validator is private and revalidated.
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "max-age=0, private, must-revalidate")
	}
	return notModified(w, r, etag, time.Time{})
}

// expiresIn is `expires_in seconds, ...`: Cache-Control max-age and the given directives, and the
// Date it was computed at.
func (s *Server) expiresIn(w http.ResponseWriter, seconds int, directives ...string) {
	w.Header().Set("Cache-Control", strings.Join(append([]string{"max-age=" + strconv.Itoa(seconds)}, directives...), ", "))
	w.Header().Set("Date", s.DB.Now().UTC().Format(http.TimeFormat))
}

// sendFile is `send_file path, type:, disposition: :inline`, named after the file.
func (s *Server) sendFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	data, err := os.ReadFile(path)
	if err != nil {
		s.fail(w, err)
		return
	}
	sendData(w, data, contentType, path[strings.LastIndexByte(path, '/')+1:])
}

// sendData is send_file's headers (send_file_headers!) and the whole body, inline.
func sendData(w http.ResponseWriter, data []byte, contentType, filename string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", storage.Disposition("inline", filename))
	h.Set("Content-Transfer-Encoding", "binary")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// respondTo is respond_to with the offered formats: the chosen one, or "" after a 406.
func (s *Server) respondTo(w http.ResponseWriter, r *http.Request, offered ...string) string {
	format, err := httpcompat.Negotiate(formatInput(r), offered...)
	if err != nil {
		publicError(w, r, http.StatusBadRequest)
		return ""
	}
	if format == "" {
		publicError(w, r, http.StatusNotAcceptable)
		return ""
	}
	return format
}

// renderAs is `render plain:/body:, content_type:`, with the Vary: Accept every render adds when
// the format came from a non-browser Accept header.
func (s *Server) renderAs(w http.ResponseWriter, r *http.Request, status int, contentType, body string) {
	h := w.Header()
	if h.Get("Vary") == "" && formatInput(r).UsesAccept() {
		h.Set("Vary", "Accept")
	}
	h.Set("Content-Type", contentType)
	w.WriteHeader(status)
	w.Write([]byte(body))
}

// redirectTo is redirect_to: 302 to an absolute URL on this host, with no body.
func (s *Server) redirectTo(w http.ResponseWriter, r *http.Request, location string) {
	if !safeRedirect(location, s.origin(r)) {
		s.fail(w, errors.New("unsafe redirect to "+location))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

// headStatus is `head status` in an action: no body, labelled with the request's format.
func headStatus(w http.ResponseWriter, r *http.Request, status int) {
	contentType := "text/html"
	if formats, _ := httpcompat.Formats(formatInput(r)); len(formats) > 0 && formats[0] != "*/*" {
		if mime := httpcompat.TypeOf(formats[0]); mime != "" {
			contentType = mime
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
}

// renderIncompatibleBrowser is AllowBrowser's `render template: "sessions/incompatible_browser"`:
// HTML whatever the format, in the controller's layout (turbo-rails' frame layout for a frame,
// except MessagesController and Messages::ByBotsController, which declare their own).
func (s *Server) renderIncompatibleBrowser(w http.ResponseWriter, r *http.Request, user *database.User) {
	build := func(ctx *views.ViewContext) views.Page { return &views.SessionsIncompatibleBrowser{Ctx: ctx} }
	if route, _, _ := recognize(r.Method, r.URL.EscapedPath()); route != nil &&
		(strings.HasPrefix(route.Endpoint, "messages#") || strings.HasPrefix(route.Endpoint, "messages/by_bots#")) {
		s.pageInAnyFormat(w, r, user, http.StatusOK, &incompatibleBrowserSize, build)
		return
	}
	s.pageOrFrameInAnyFormat(w, r, user, http.StatusOK, &incompatibleBrowserSize, build)
}

// paramString is params[key] as a string (the reference's param_str): absent or null is false.
func paramString(r *http.Request, key string) (string, bool) {
	if !r.Form.Has(key) || nullParam(r, key) {
		return "", false
	}
	return r.Form.Get(key), true
}

// hasNestedParams is whether params[name] is present as a hash (params.require(name)).
func hasNestedParams(r *http.Request, name string) bool {
	for key := range r.Form {
		if strings.HasPrefix(key, name+"[") {
			return true
		}
	}
	if r.MultipartForm != nil {
		for key := range r.MultipartForm.File {
			if strings.HasPrefix(key, name+"[") {
				return true
			}
		}
	}
	return false
}

// truncatedPassword is the first 72 bytes of a password, all bcrypt reads.
func truncatedPassword(password string) []byte {
	if len(password) > 72 {
		return []byte(password[:72])
	}
	return []byte(password)
}
