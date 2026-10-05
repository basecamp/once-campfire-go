package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/views"
)

const HealthBody = `<!DOCTYPE html><html><body style="background-color: green"></body></html>`
const MaxBody = 16 << 20

type Server struct {
	// views is the fragment store the reference's templates cache into (views.FragmentCache).
	views      *views.FragmentCache
	Webhooks   *integrations.WebhookClient
	Jobs       *jobs.Runner
	Push       *integrations.PushSender
	Unfurler   *integrations.Unfurler
	Storage    *storage.Store
	Cable      *cable.Hub
	DB         *database.DB
	Secrets    *rails.Secrets
	Secure     bool
	mux        *router
	attemptsMu sync.Mutex
	attempts   map[string]attempt
	dummyHash  []byte
}
type attempt struct {
	Count int
	Start time.Time
}

func New(db *database.DB, secrets *rails.Secrets, secure bool, storagePaths ...string) (*Server, error) {
	// Same cost-12 dummy digest as reference/crates/db/src/models/user.rs.
	// Unknown-user login still pays bcrypt; startup need not create a new hash.
	hash := []byte("$2a$12$FiKmSp4UhLvSB4Sd/ZUjQunyKP6.NjDRHdr5LnKUVk.BUn4Mq12WS")
	cacheMB := 32
	if raw, ok := os.LookupEnv("CAMPFIRE_FRAGMENT_CACHE_MB"); ok {
		var err error
		cacheMB, err = strconv.Atoi(raw)
		if err != nil || cacheMB < 0 || cacheMB > 1<<20 {
			return nil, fmt.Errorf("invalid CAMPFIRE_FRAGMENT_CACHE_MB %q", raw)
		}
	}
	s := &Server{views: views.NewFragmentCache(cacheMB << 20), Cable: cable.New(db, secrets), DB: db, Secrets: secrets, Secure: secure, mux: &router{}, attempts: map[string]attempt{}, dummyHash: hash}
	storageRoot := "storage"
	if len(storagePaths) > 0 {
		storageRoot = storagePaths[0]
	}
	s.Storage = storage.New(db, secrets, storageRoot)
	s.DB.ResetConnections = s.Cable.Reconnect
	s.DB.PlainText = func(body string) string { return s.plainText(context.Background(), body) }
	s.registerStorageRoutes()
	s.Unfurler = integrations.NewUnfurler()
	s.Webhooks = integrations.NewWebhookClient()
	s.initJobs()
	s.mux.HandleFunc("POST /unfurl_link", s.auth(s.unfurl))
	s.registerPWARoutes()
	s.mux.HandleFunc("GET /qr_code/{code}", s.browserCheck(s.qrCodeShow))
	s.mux.HandleFunc("GET /autocompletable/users", s.auth(s.autocompletableUsersIndex))
	s.mux.HandleFunc("GET /cable", s.auth(s.serveCable))
	s.mux.HandleFunc("GET /up", s.health)
	s.mux.HandleFunc("GET /session/new", s.browserCheck(s.sessionsNew))
	s.mux.HandleFunc("POST /session", s.browserCheck(s.sessionsCreate))
	s.mux.HandleFunc("DELETE /session", s.auth(s.sessionsDestroy))
	s.mux.HandleFunc("GET /first_run", s.browserCheck(s.firstRunsShow))
	s.mux.HandleFunc("POST /first_run", s.browserCheck(s.firstRunsCreate))
	s.mux.HandleFunc("GET /{$}", s.auth(s.welcomeShow))
	s.mux.HandleFunc("GET /rooms/{id}", s.auth(s.roomShow))
	s.mux.HandleFunc("GET /rooms/{room_id}/messages", s.auth(s.messagesIndex))
	s.mux.HandleFunc("POST /rooms/{room_id}/messages", s.auth(s.messagesCreate))
	s.mux.HandleFunc("GET /users/{user_id}/sidebar", s.auth(s.usersSidebarShow))
	s.registerMessageRoutes()
	s.registerRoomRoutes()
	s.registerMediaRoutes()
	s.registerAccountRoutes()
	s.mux.HandleFunc("GET /searches", s.auth(s.searchesIndex))
	s.mux.HandleFunc("POST /searches", s.auth(s.searchesCreate))
	s.mux.HandleFunc("DELETE /searches/clear", s.auth(s.searchesClear))
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(context.WithValue(r.Context(), requestHostKey{}, r.Host))
	r = r.WithContext(context.WithValue(r.Context(), requestOriginKey{}, s.origin(r)))
	if assets.Serve(w, r) {
		return
	}
	if r.URL.Path != "/cable" && !strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
		buffered := &responseBuffer{ResponseWriter: w}
		w = buffered
		defer func() { buffered.finish(r) }()
	}
	w, r = s.withBrowserSession(w, r)
	defer func() {
		if sw := w.(*sessionWriter); !sw.written {
			sw.WriteHeader(200)
		}
	}()
	if _, err := requestRemoteIP(r); err != nil {
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
	if r.Method != "GET" && r.Method != "HEAD" && !strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
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
	if route, _, _ := recognize(r.Method, r.URL.EscapedPath()); route != nil && route.bot {
		s.routeHTTP(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		limit := int64(MaxBody)
		if multipartBoundary(r) != "" {
			limit = maxMultipartBody
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if !s.sameOrigin(r) {
			http.Error(w, "Invalid request origin", 422)
			return
		}
		if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/rails/active_storage/disk/") {
			s.routeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
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
		if err := parseJSONParams(r); err != nil {
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
		normalizeScalarParams(r)
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
	normalizeScalarParams(r)
	s.routeHTTP(w, r)
}
func (s *Server) sameOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site == "cross-site" || s.Secure && (site != "same-origin" && site != "same-site") {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme+"://"+u.Host != s.origin(r) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
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
func (s *Server) fail(w http.ResponseWriter, err error) {
	status := 500
	if errors.Is(err, database.ErrNoRows) {
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
func (s *Server) auth(next func(http.ResponseWriter, *http.Request, database.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var token string
		c, err := r.Cookie("session_token")
		if err == nil {
			err = s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(c.Value), s.DB.Now(), &token)
		}
		if err != nil || token == "" {
			s.requestAuthentication(w, r)
			return
		}
		// The reference's resume_session: the session by its token, its activity refreshed at most
		// hourly (only then on the writer, with a fresh cookie), then its user.
		session, found, err := s.DB.SessionByToken(r.Context(), token)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !found {
			s.requestAuthentication(w, r)
			return
		}
		if session.NeedsResume(s.DB.Now()) {
			agent, ip := r.UserAgent(), remoteIP(r)
			if err = s.DB.ResumeSession(r.Context(), session, &agent, &ip); err != nil {
				s.fail(w, err)
				return
			}
			if err = s.setAuthenticationCookie(w, token); err != nil {
				s.fail(w, err)
				return
			}
		}
		u, found, err := s.DB.UserFindByID(r.Context(), session.UserID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !found {
			s.requestAuthentication(w, r)
			return
		}
		if s.blockBrowserFor(w, r, &u) {
			return
		}
		next(w, r, u)
	}
}
func roomID(r *http.Request) int64 {
	value := r.PathValue("room_id")
	if value == "" {
		value = r.Form.Get("room_id")
	}
	if value == "" {
		value = r.PathValue("id")
	}
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

func (s *Server) serveCable(w http.ResponseWriter, r *http.Request, u database.User) {
	if !s.sameOrigin(r) {
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
func (s *Server) Close() { s.Jobs.Close(10 * time.Second); s.Cable.Close() }

func appVersion() string {
	for _, key := range []string{"APP_VERSION", "GIT_REVISION"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return "Go"
}
