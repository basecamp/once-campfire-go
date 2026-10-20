package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/useragent"
	"golang.org/x/crypto/bcrypt"
)

const (
	HealthBody = `<!DOCTYPE html><html><body style="background-color: green"></body></html>`
	MaxBody    = 16 << 20
)

// Dependencies are prepared by the process owner. HTTP does not open resources,
// construct application services, or start background workers.
type Dependencies struct {
	Fragments           *presentation.Fragments
	MessageEffects      *application.MessageEffects
	MessagePublications *application.MessagePublications
	Push                *integrations.PushSender
	Unfurler            *integrations.Unfurler
	Storage             *storage.Store
	MessageCommands     *application.Messages
	BoostCommands       *application.Boosts
	MessageQueries      *application.MessageQueries
	PageQueries         *application.PageQueries
	BotQueries          *application.BotQueries
	AccountQueries      *application.AccountQueries
	NotificationQueries *application.NotificationQueries
	Searches            *application.Searches
	RoomCommands        *application.Rooms
	RoomQueries         *application.RoomQueries
	RoomPublications    *application.RoomPublications
	AccountCommands     *application.Accounts
	SessionCommands     *application.Sessions
	Cable               *cable.Hub
	DB                  *database.DB
	Secrets             *rails.Secrets
	Presentation        *presentation.Renderer
}

type Config struct {
	Secure             bool
	ResponseCacheBytes int
}
type Server struct {
	Dependencies
	Secure     bool
	responses  *responseCache
	dispatch   []http.HandlerFunc
	intakeMu   sync.Mutex
	closing    bool
	handlers   sync.WaitGroup
	attemptsMu sync.Mutex
	attempts   map[string]attempt
	dummyHash  []byte
}
type attempt struct {
	Count int
	Start time.Time
}
type page struct {
	// An already prepared immutable message list; rendering never loads records.
	messageBody *responsebody.Part

	MessagesHTML                 template.HTML
	Version                      string
	UserDivider                  int
	BackPath                     string
	Invitation                   bool
	Placeholders                 []database.RoomParticipant
	NextPage                     int64
	Administrators               []database.AccountMember
	AccountUsers                 []database.AccountMember
	Bots                         []presentation.BotView
	Bot                          database.BotIdentity
	Platform                     useragent.Platform
	Frame                        bool
	SidebarRooms                 []presentation.RoomView
	RoomsStream, UserRoomsStream string
	AvatarAttached               bool
	AvatarURL                    string
	Memberships                  []presentation.RoomView
	DirectMemberships            []presentation.RoomView
	Screen                       string
	ReturnRoom                   int64
	Email                        string
	HelpContact                  database.UserContact
	Reload                       bool
	Chat                         bool
	Notice                       string
	VAPIDPublicKey               string
	Subscriptions                []database.PushSubscription
	RecentSearches               []string
	Subject                      database.UserProfile
	JoinCode, Webhook, Transfer  string
	RoomUsers                    []database.RoomChoice
	CanAdminister                bool
	Involvement                  string
	Account                      database.Account
	CustomStyles                 template.HTML
	BodyClass, LoadedAt          string
	Origin                       string
	CanCreateRooms               bool
	Stream                       string
	Title, Error                 string
	User                         database.User
	Room                         database.Room
	Rooms                        []database.Room
	Messages                     []presentation.MessageView
	Setup                        bool
	Query                        string
	SearchResultCount            int
}

func New(deps Dependencies, config Config) *Server {
	// Same cost-12 dummy digest as the reference. Unknown-user login still
	// pays bcrypt; startup need not create a new hash.
	s := &Server{
		Dependencies: deps,
		Secure:       config.Secure,
		responses:    newResponseCache(config.ResponseCacheBytes),
		attempts:     map[string]attempt{},
		dummyHash:    []byte("$2a$12$FiKmSp4UhLvSB4Sd/ZUjQunyKP6.NjDRHdr5LnKUVk.BUn4Mq12WS"),
	}
	s.dispatch = s.bindRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.intakeMu.Lock()
	if s.closing {
		s.intakeMu.Unlock()
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	s.handlers.Add(1)
	s.intakeMu.Unlock()
	defer s.handlers.Done()
	r = s.normalizeRequest(r)
	if assets.Serve(w, r) {
		return
	}
	if r.URL.Path != "/cable" && !strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
		buffered := &responseBuffer{ResponseWriter: w, server: s}
		w = buffered
		defer func() { buffered.finish(r) }()
	}
	w, r = s.withBrowserSession(w, r)
	s.beginResponseCache(r)
	defer func() {
		if sw := w.(*sessionWriter); !sw.written {
			sw.WriteHeader(200)
		}
	}()
	if requestMetadata(r.Context()).ipError != nil {
		http.Error(w, "IP spoofing attack", 500)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
		w.Header().Set("X-Version", appVersion())
		revision := os.Getenv("GIT_REVISION")
		if revision == "" {
			revision = "0"
		}
		w.Header().Set("X-Rev", revision)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("X-XSS-Protection", "0")
	w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
	if r.Method != "GET" && r.Method != "HEAD" &&
		!strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
		banned, err := s.DB.BannedIP(r.Context(), remoteIP(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		if banned {
			w.WriteHeader(429)
			return
		}
	}
	if route, _, _ := recognizeRequest(r); route != nil && route.bot {
		s.routeHTTP(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		limit := int64(MaxBody)
		if multipartBoundary(r) != "" {
			limit = maxMultipartBody
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		// The disk PUT authenticates a session and a signed upload capability;
		// direct-upload creation and every other browser write use Fetch Metadata.
		if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/rails/active_storage/disk/") {
			s.routeHTTP(w, r)
			return
		}
		if !s.browserWriteAllowed(r) {
			http.Error(w, "Invalid request origin", 422)
			return
		}
		if err := parseRequestForm(r); err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(w, "Request too large", 413)
			} else {
				http.Error(w, "Invalid form", 400)
			}
			return
		}
		if boundary := multipartBoundary(r); boundary != "" {
			cleanup, err := parseMultipart(r, boundary)
			defer cleanup()
			if err != nil {
				var limit *http.MaxBytesError
				if errors.As(err, &limit) {
					http.Error(w, "Request too large", 413)
				} else {
					http.Error(w, "Invalid upload", 400)
				}
				return
			}
		}
		if err := parseRequestParams(r); err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(w, "Request too large", 413)
			} else {
				http.Error(w, "Invalid JSON", 400)
			}
			return
		}
		for key, values := range r.URL.Query() {
			r.Form[key] = values
		}
		if err := normalizeScalarParams(r); err != nil {
			http.Error(w, "Invalid query", 400)
			return
		}
		if r.Method == "POST" {
			switch strings.ToUpper(r.PostForm.Get("_method")) {
			case "PATCH":
				r.Method = "PATCH"
			case "PUT":
				r.Method = "PUT"
			case "DELETE":
				r.Method = "DELETE"
			}
		}
	}
	if r.Form == nil {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid query", 400)
			return
		}
	}
	if err := normalizeScalarParams(r); err != nil {
		http.Error(w, "Invalid query", 400)
		return
	}
	s.routeHTTP(w, r)
}

// Matches Rust's token-free unsafe-request policy. An explicitly empty header is
// malformed, not the plain-HTTP compatibility case where the header is absent.
func (s *Server) browserWriteAllowed(r *http.Request) bool {
	if values, provided := r.Header["Origin"]; provided && (len(values) == 0 || values[0] != s.origin(r)) {
		return false
	}
	values, provided := r.Header["Sec-Fetch-Site"]
	if !provided {
		return !requestMetadata(r.Context()).https
	}
	return len(values) > 0 && (values[0] == "same-origin" || values[0] == "same-site")
}

// Socket handshakes retain their origin policy independently of unsafe browser writes.
func (s *Server) socketOriginAllowed(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site == "cross-site" || s.Secure && (site != "same-origin" && site != "same-site") {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme+"://"+u.Host != s.origin(r) || u.User != nil ||
			u.RawQuery != "" ||
			u.Fragment != "" ||
			u.Path != "" {
			return false
		}
	}
	return true
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	format := respondFormat(w, r, "html", "json")
	if format == "" {
		return
	}
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(struct {
			Status    string `json:"status"`
			Timestamp string `json:"timestamp"`
		}{"up", time.Now().UTC().Format(time.RFC3339)})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, HealthBody)
}

// respondPage prepares common application data and live protocol facts before rendering.
func (s *Server) respondPage(w http.ResponseWriter, r *http.Request, name string, status int, p page) {
	if name != "incompatible-browser" && respondFormat(w, r, "html") == "" {
		return
	}
	layout, err := s.PageQueries.Layout(r.Context(), application.LayoutRequest{
		UserID:     p.User.ID,
		Help:       name == "login" || name == "join",
		Navigation: name == "room-form" || name == "account" || name == "push-subscriptions" || name == "search",
		LastRoom:   lastRoomCandidate(r),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	notice, alert := s.consumeFlash(r)
	p.Notice = notice
	if p.Error == "" {
		p.Error = alert
	}
	a := layout.Account
	p.Account = a
	p.Email = r.Form.Get("email_address")
	if name == "login" || name == "join" {
		p.Reload = true
		p.HelpContact = layout.HelpContact
	}
	p.BackPath = "/"
	if name == "room-form" || name == "account" || name == "push-subscriptions" {
		if layout.ReturnRoom != nil {
			p.BackPath = fmt.Sprintf("/rooms/%d", *layout.ReturnRoom)
		}
	}
	if layout.ReturnRoom != nil {
		p.ReturnRoom = *layout.ReturnRoom
	}
	p.Version = appVersion()
	if r.Header.Get("Turbo-Frame") != "" && name != "edit-message" && name != "show-message" &&
		name != "incompatible-browser" &&
		name != "room-not-found" {
		p.Frame = true
	}
	p.Platform = requestAgent(r).View()
	p.Screen = name
	p.Chat = name == "room" && p.Room.ID != 0
	if s.Push.VAPID != nil {
		p.VAPIDPublicKey = s.Push.VAPID.PublicKey()
	}
	// Keep the refresh cursor at the room version read before the message query.
	// A render-time clock could skip a message committed between query and render.
	p.LoadedAt = strconv.FormatInt(p.Room.UpdatedAt.UnixMilli(), 10)
	p.Origin = s.origin(r)
	p.CanCreateRooms = p.User.Role == 1 || !a.RestrictRooms()
	if p.Chat || name == "search" || name == "welcome" {
		p.BodyClass = "sidebar"
	}
	if name == "search" {
		p.BodyClass += " searches"
	}
	if p.Setup || name == "join" {
		p.BodyClass = "signup"
	}
	if a.CustomStyles != "" {
		p.CustomStyles = template.HTML("<style>" + a.CustomStyles + "</style>")
	}
	s.renderPage(w, name, status, p)
}

// renderPage consumes prepared data only, without consulting a request or query service.
func (s *Server) renderPage(w http.ResponseWriter, name string, status int, p page) {
	recorded := p.messageBody
	p.messageBody = nil
	if (name == "room" || name == "search") && recorded != nil {
		var parts []responsebody.Part
		var err error
		if name == "room" {
			parts, err = s.Fragments.RoomParts(layoutInput(p), p.LoadedAt, *recorded)
		} else {
			parts, err = s.Fragments.SearchParts(presentation.SearchInput{LayoutInput: layoutInput(p), Query: p.Query, SearchResultCount: p.SearchResultCount, RecentSearches: p.RecentSearches, ReturnRoom: p.ReturnRoom}, *recorded)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeParts(w, status, parts)
		return
	}
	if recorded != nil {
		p.MessagesHTML = template.HTML("\x00campfire-" + rand.Text() + "\x00")
	}
	if name == "sidebar" {
		parts, err := s.Fragments.SidebarParts(layoutInput(p), sidebarInput(p))
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeParts(w, status, parts)
		return
	}
	b := borrowBuffer()
	defer releaseBuffer(b)
	if err := s.Presentation.ExecuteTemplate(b, name, p); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if recorded != nil {
		writeRecorded(w, status, b.String(), string(p.MessagesHTML), *recorded)
		return
	}
	w.WriteHeader(status)
	w.Write(b.Bytes())
}
func (s *Server) fail(w http.ResponseWriter, err error) {
	status := 500
	if errors.Is(err, sql.ErrNoRows) {
		status = 404
	} else if errors.Is(err, database.ErrForbidden) {
		status = 403
	} else {
		slog.Error("request failed", "error", err)
	}
	if writer, ok := w.(*sessionWriter); ok {
		publicError(w, writer.session.request, status)
	} else {
		http.Error(w, http.StatusText(status), status)
	}
}

func (s *Server) auth(
	next func(http.ResponseWriter, *http.Request, database.User),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var token string
		c, err := r.Cookie("session_token")
		if err == nil {
			err = s.Secrets.VerifyCookie(
				"session_token",
				rails.UnescapeCookie(c.Value),
				s.DB.Now(),
				&token,
			)
		}
		if err != nil || token == "" {
			s.requestAuthentication(w, r)
			return
		}
		u, refreshed, err := s.DB.AuthenticateSession(r.Context(), token, r.UserAgent(), remoteIP(r))
		if errors.Is(err, sql.ErrNoRows) {
			s.requestAuthentication(w, r)
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		if refreshed {
			if err = s.setAuthenticationCookie(w, token); err != nil {
				s.fail(w, err)
				return
			}
		}
		if s.blockBrowser(w, r) {
			return
		}
		if info := requestMetadata(r.Context()); info != nil && info.response != nil {
			info.response.user = u.ID
		}
		next(w, r, u)
	}
}

func (s *Server) hasAccount(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.Read.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&n)
	return n > 0, err
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	exists, err := s.hasAccount(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if !exists {
		http.Redirect(w, r, "/first_run", 302)
		return
	}
	s.respondPage(w, r, "login", 200, page{Title: "Sign in"})
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	exists, err := s.hasAccount(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if exists {
		http.Redirect(w, r, "/", 302)
		return
	}
	s.respondPage(w, r, "first-run", 200, page{Title: "Set up Campfire", Setup: true})
}

func (s *Server) allowLogin(ip string) bool {
	s.attemptsMu.Lock()
	defer s.attemptsMu.Unlock()
	now := s.DB.Now()
	for k, a := range s.attempts {
		if now.Sub(a.Start) >= 3*time.Minute {
			delete(s.attempts, k)
		}
	}
	a := s.attempts[ip]
	if a.Start.IsZero() {
		if len(s.attempts) >= 10000 {
			return false
		}
		a.Start = now
	}
	a.Count++
	s.attempts[ip] = a
	return a.Count <= 10
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(remoteIP(r)) {
		s.respondPage(
			w,
			r,
			"login",
			429,
			page{Title: "Sign in", Error: "Too many requests or unauthorized."},
		)
		return
	}
	u, err := s.DB.UserByEmail(r.Context(), r.Form.Get("email_address"))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	hash := []byte(u.Password)
	if err != nil {
		hash = s.dummyHash
	}
	valid := bcrypt.CompareHashAndPassword(hash, []byte(r.Form.Get("password"))) == nil
	if err != nil || !valid {
		s.respondPage(
			w,
			r,
			"login",
			401,
			page{Title: "Sign in", Error: "Too many requests or unauthorized."},
		)
		return
	}
	s.startSession(w, r, u)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	password := r.Form.Get("user[password]")
	if password == "" || len(password) > 72 {
		s.respondPage(
			w,
			r,
			"first-run",
			422,
			page{
				Title: "Set up Campfire",
				Setup: true,
				Error: "Password must contain 1 to 72 bytes.",
			},
		)
		return
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		s.fail(w, err)
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
	result, err := s.AccountCommands.Setup(
		r.Context(),
		r.Form.Get("user[name]"),
		r.Form.Get("user[email_address]"),
		string(digest),
		application.Attachment{File: upload},
	)
	if errors.Is(err, database.ErrForbidden) {
		http.Redirect(w, r, "/", 302)
		return
	}
	if errors.Is(err, database.ErrValidation) {
		s.respondPage(
			w,
			r,
			"first-run",
			422,
			page{
				Title: "Set up Campfire",
				Setup: true,
				Error: "Name and email address are required.",
			},
		)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
	s.startSession(w, r, result.Commit.User)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u database.User) {
	token, err := s.SessionCommands.Start(r.Context(), u.ID, r.UserAgent(), remoteIP(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.setAuthenticationCookie(w, token); err != nil {
		s.fail(w, err)
		return
	}
	location := s.postAuthenticationURL(r)
	if !safeRedirect(location, s.origin(r)) {
		s.fail(w, errors.New("unsafe authentication redirect"))
		return
	}
	http.Redirect(w, r, location, 302)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, u database.User) {
	c, err := r.Cookie("session_token")
	if err != nil {
		s.fail(w, err)
		return
	}
	var token string
	if err = s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(c.Value), s.DB.Now(), &token); err != nil {
		s.fail(w, err)
		return
	}
	if err = s.SessionCommands.End(r.Context(), u.ID, token, r.Form.Get("push_subscription_endpoint")); err != nil {
		s.fail(w, err)
		return
	}
	browserState(r).reset()
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     "session_token",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		},
	)
	http.Redirect(w, r, "/", 302)
}

// Cookie parsing is protocol-owned; candidate authorization and fallback are application-owned.
func lastRoomCandidate(r *http.Request) *int64 {
	if cookie, err := r.Cookie("last_room"); err == nil {
		if id, err := strconv.ParseInt(cookie.Value, 10, 64); err == nil {
			return &id
		}
	}
	return nil
}
func (s *Server) home(w http.ResponseWriter, r *http.Request, u database.User) {
	id, err := s.PageQueries.LastRoom(r.Context(), u.ID, lastRoomCandidate(r))
	if errors.Is(err, sql.ErrNoRows) {
		s.respondPage(w, r, "welcome", 200, page{Title: "No rooms yet", User: u})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/rooms/%d", s.origin(r), id), 302)
}

func roomID(r *http.Request) int64 {
	value := r.PathValue("room_id")
	if value == "" {
		value = r.Form.Get("room_id")
	}
	if value == "" {
		route, _, _ := recognizeRequest(r)
		if route == nil || !strings.HasPrefix(route.Endpoint, "messages#") {
			value = r.PathValue("id")
		}
	}
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}
func (s *Server) room(w http.ResponseWriter, r *http.Request, u database.User) {
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.roomLookupFailure(w, r, err)
		return
	}
	if hit := s.responseHit(r); hit != nil {
		s.rememberRoom(w, r, strconv.FormatInt(room.ID, 10))
		hit.serve(w)
		return
	}
	anchor, _ := strconv.ParseInt(r.PathValue("message_id"), 10, 64)
	messageBody, _, err := s.MessageQueries.Page(r.Context(), s.messageScope(r.Context()), u.ID, room.ID, anchor, "around", true)
	if err != nil {
		s.fail(w, err)
		return
	}
	roomPage, err := s.PageQueries.RoomPage(r.Context(), room, u.Participant())
	if err != nil {
		s.fail(w, err)
		return
	}
	room = roomPage.Room.Room
	s.rememberRoom(w, r, strconv.FormatInt(room.ID, 10))
	s.respondPage(
		w,
		r,
		"room",
		200,
		page{
			Invitation:  roomPage.Invitation,
			Stream:      s.Secrets.SignStream(rails.RoomStream(room.Type, room.ID)),
			Title:       room.Name,
			User:        u,
			Room:        room,
			messageBody: &messageBody,
		},
	)
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, u database.User) {
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	anchor, direction, _, err := messagePagination(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if hit := s.responseHit(r); hit != nil {
		hit.serve(w)
		return
	}
	messageBody, count, err := s.MessageQueries.Page(r.Context(), s.messageScope(r.Context()), u.ID, room.ID, anchor, direction, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count == 0 {
		w.WriteHeader(204)
		return
	}
	// The rendered representation, including related users and boosts, defines
	// freshness. Timestamps alone miss external edits and association changes.
	s.respondPage(w, r, "messages", 200, page{messageBody: &messageBody})
}

func (s *Server) createMessage(w http.ResponseWriter, r *http.Request, u database.User) {
	if !requireMessage(w, r) {
		return
	}
	if _, err := s.DB.Room(r.Context(), u.ID, roomID(r)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.respondPage(w, r, "room-not-found", 200, page{User: u})
			return
		}
		s.fail(w, err)
		return
	}
	var staged *storage.Staged
	var err error
	if r.MultipartForm != nil && len(r.MultipartForm.File["message[attachment]"]) > 0 {
		staged, err = s.stageAttachment(r, "message[attachment]")
		if err != nil {
			s.fail(w, err)
			return
		}
	} else if r.Form.Get("message[attachment]") != "" {
		s.fail(w, errors.New("could not find or build blob: expected attachable"))
		return
	}
	var body *string
	if r.Form.Has("message[body]") && !nullParam(r, "message[body]") {
		value := r.Form.Get("message[body]")
		body = &value
	}
	result, err := s.MessageCommands.Create(
		r.Context(),
		u.ID,
		roomID(r),
		r.Form.Get("message[client_message_id]"),
		body,
		staged,
	)
	if err != nil {
		s.fail(w, err)
		return
	}

	output, err := s.MessageEffects.Created(r.Context(), s.presentationFacts(r.Context()), result)
	if err != nil {
		s.fail(w, err)
		return
	}
	if respondFormat(w, r, "turbo_stream") != "" {
		writeStream(w, output)
	}
}

func (s *Server) sidebar(w http.ResponseWriter, r *http.Request, u database.User) {
	if hit := s.responseHit(r); hit != nil {
		hit.serve(w)
		return
	}
	data, err := s.PageQueries.Sidebar(r.Context(), u.Participant())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.respondPage(
		w,
		r,
		"sidebar",
		200,
		page{
			Placeholders:    data.Placeholders,
			SidebarRooms:    data.Rooms,
			User:            u,
			RoomsStream:     s.Secrets.SignStream("rooms"),
			UserRoomsStream: s.Secrets.SignStream(rails.UserRoomsStream(u.ID)),
		},
	)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, u database.User) {
	if r.Method == "POST" {
		q, err := s.Searches.Remember(r.Context(), u.ID, r.FormValue("q"))
		if err != nil {
			s.fail(w, err)
			return
		}
		http.Redirect(w, r, "/searches?q="+url.QueryEscape(q), 302)
		return
	}
	if r.Method == "DELETE" {
		if err := s.Searches.Clear(r.Context(), u.ID); err != nil {
			s.fail(w, err)
			return
		}
		http.Redirect(w, r, "/searches", 302)
		return
	}
	if hit := s.responseHit(r); hit != nil {
		hit.serve(w)
		return
	}
	data, err := s.Searches.Page(r.Context(), s.messageScope(r.Context()), u.ID, r.FormValue("q"))
	if err != nil {
		s.fail(w, err)
		return
	}
	p := page{Title: "Search", Query: data.Query, User: u, RecentSearches: data.Recent, SearchResultCount: data.Count}
	if data.Count > 0 {
		p.messageBody = &data.Messages
	}
	s.respondPage(w, r, "search", 200, p)
}

func (s *Server) serveCable(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.socketOriginAllowed(r) {
		http.Error(w, "Invalid request origin", 403)
		return
	}
	c, err := r.Cookie("session_token")
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	var token string
	if err = s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(c.Value), s.DB.Now(), &token); err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	s.Cable.Serve(w, r, u, token)
}

// StopIntake is the cutoff for handler admission. The process owner closes
// upgraded sockets before WaitHandlers, then joins workers before persistence.
func (s *Server) StopIntake() {
	s.intakeMu.Lock()
	s.closing = true
	s.intakeMu.Unlock()
}
func (s *Server) WaitHandlers() { s.handlers.Wait() }
func appVersion() string {
	for _, key := range []string{"APP_VERSION", "GIT_REVISION"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return "Go"
}
