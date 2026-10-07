package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastdb"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/piececache"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/richtext"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/useragent"
	"golang.org/x/crypto/bcrypt"
)

const HealthBody = `<!DOCTYPE html><html><body style="background-color: green"></body></html>`
const MaxBody = 16 << 20

// parseRecordedPieces maps a CAMPFIRE_RECORDED_PIECES value to its setting. It
// reports valid=false for an unrecognised value so the caller can warn while
// keeping the default on rather than silently changing behaviour.
func parseRecordedPieces(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

type Server struct {
	fragments *fragmentCache
	// fastRender is the compiled message-fragment renderer (ENGINE-32). nil
	// when CAMPFIRE_FAST_RENDER=off or the fragment compile failed; messageViews
	// then falls back to html/template's message-uncached.
	fastRender *messageRenderer
	// fastdb is the pooled fast read layer for the hot read paths
	// (CAMPFIRE_FASTDB=off leaves it nil and the handlers use database/sql).
	fastdb *fastdb.Pool
	// refsCache holds message-page reference windows keyed by room version
	// (CAMPFIRE_MESSAGE_REFS_CACHE_MB=0 leaves it storing nothing and every
	// lookup misses, keeping the handlers on the scan path).
	refsCache *messageRefsCache
	// messageRefsHits/Misses count reference-cache lookups so tests can pin
	// invalidation and the warm path.
	messageRefsHits   atomic.Int64
	messageRefsMisses atomic.Int64
	// authFast enables the auth/session fast path (ENGINE-42,
	// CAMPFIRE_AUTH_FAST=off disables it): a bounded cache of verified
	// session_token cookie values with their signed expiry, and the joined
	// session+user read that gates the hourly RefreshSession write in Go.
	authFast bool
	// authCache is the verified-cookie cache; nil when authFast is off.
	authCache *verifiedCookieCache
	// avatars is the signed avatar URL cache shared by the template funcs
	// and the compiled fragment renderer (ENGINE-45b).
	avatars *avatarCache
	// pieces stores recorded-response pieces (raw + deflate fragment + digest)
	// under content-versioned keys, sized by CAMPFIRE_RECORDED_CACHE_MB.
	pieces         *piececache.Cache
	recordedPieces bool
	// zstdPieces enables the zstd frame variant of cached pieces (ENGINE-50;
	// off by default since ENGINE-51 because Chromium decodes only the first
	// frame of a multi-frame stream, and the piece path is always
	// multi-piece — see the flag comment in New): fills also store a complete
	// zstd frame per piece, and only single-frame shapes are served zstd.
	zstdPieces bool
	// precomposed enables the ENGINE-49 precomposed-framing path
	// (CAMPFIRE_PRECOMPOSED_FRAMING=off disables it): recorded responses are
	// emitted as one precomputed head block plus body parts, skipping the
	// http.Header map and per-request header strings.
	precomposed bool
	// arenaOn enables the ENGINE-48 per-request arena
	// (CAMPFIRE_REQUEST_ARENA=off disables it): response buffers, parts and
	// the precomposed head and body come from the request's reclaimed block.
	arenaOn bool
	// xVersion and xRev are the process-constant header values captured at
	// startup, so the precomposed head block can embed them without a
	// per-request env lookup.
	xVersion, xRev string
	// readCache holds the ENGINE-43/44 read caches: room+membership rows,
	// the account row, the invitation probe and the original-room fallback,
	// keyed by the version counters they depend on
	// (CAMPFIRE_READ_CACHE=off leaves it nil and every lookup misses).
	readCache *readCache
	// logoVersion counts account-logo detachments (DELETE /account/logo),
	// the one account-visible write that touches neither the accounts row
	// nor any sidebar-visible table, so the account read cache keys on it.
	logoVersion atomic.Int64
	// searchCache stores search pages keyed by (user, query, corpus,
	// membership versions) so hits skip Search, Rooms and RecentSearches
	// (CAMPFIRE_SEARCH_CACHE=off leaves it nil and the handler keeps the
	// database/sql reads).
	searchCache *searchResultCache
	// recordedAssemblies counts gzip piece-path assemblies so tests can pin
	// that a 304 never assembles a body. recordedShellFallbacks counts
	// requests that fell back to the legacy render because the shell could not
	// be split into pieces; recordedShellWarned fires the one-time warning.
	recordedAssemblies     atomic.Int64
	recordedShellFallbacks atomic.Int64
	recordedShellWarned    atomic.Bool
	Webhooks               *integrations.WebhookClient
	Jobs                   *jobs.Runner
	Push                   *integrations.PushSender
	Unfurler               *integrations.Unfurler
	Storage                *storage.Store
	Cable                  *cable.Hub
	DB                     *database.DB
	Secrets                *rails.Secrets
	Secure                 bool
	mux                    *router
	templates              *template.Template
	attemptsMu             sync.Mutex
	attempts               map[string]attempt
	dummyHash              []byte
}
type attempt struct {
	Count int
	Start time.Time
}
type profileMembership struct {
	Room        database.Room
	Involvement string
}
type botView struct {
	User  database.User
	Rooms []database.Room
}
type page struct {
	MessagesHTML                 template.HTML
	Version                      string
	UserDivider                  int
	BackPath                     string
	Invitation                   bool
	Placeholders                 []database.User
	NextPage                     int64
	Administrators               []database.User
	Bots                         []botView
	Platform                     useragent.Platform
	Frame                        bool
	SidebarRooms                 []sidebarRoom
	RoomsStream, UserRoomsStream string
	AvatarAttached               bool
	AvatarURL                    string
	Memberships                  []profileMembership
	DirectMemberships            []profileMembership
	Screen                       string
	ReturnRoom                   int64
	Email                        string
	HelpContact                  database.User
	Reload                       bool
	Chat                         bool
	Notice                       string
	VAPIDPublicKey               string
	Subscriptions                []database.PushSubscription
	RecentSearches               []string
	Subject                      database.User
	JoinCode, Webhook, Transfer  string
	Users                        []database.User
	Selected                     map[int64]bool
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
	Messages                     []messageView
	Setup                        bool
	Query                        string
	// sidebarKey carries the version-keyed fragment key from Server.sidebar
	// to render, which stores the rendered fragment under it on a miss. It is
	// unexported: only the sidebar handler sets it, and the render's sidebar
	// branch is the only reader.
	sidebarKey string
}
type messageView struct {
	AllEmoji                         bool
	Fragment                         template.HTML
	Attachment                       *storage.Blob
	BlobURL, DownloadURL, PreviewURL string
	Image                            bool
	database.Message
	Editable         string
	HTML             template.HTML
	Permalink        string
	CreatorTitle     string
	CreatorUpdatedAt time.Time
	RoomName         string
	Boosts           []database.Boost
}

func New(db *database.DB, secrets *rails.Secrets, secure bool, dbPath string, storagePaths ...string) (*Server, error) {
	// Same cost-12 dummy digest as reference/crates/db/src/models/user.rs.
	// Unknown-user login still pays bcrypt; startup need not create a new hash.
	hash := []byte("$2a$12$FiKmSp4UhLvSB4Sd/ZUjQunyKP6.NjDRHdr5LnKUVk.BUn4Mq12WS")
	avatars := newAvatarCache()
	t, err := parseTemplates(secrets, avatars)
	if err != nil {
		return nil, err
	}
	cacheMB := 32
	if raw, ok := os.LookupEnv("CAMPFIRE_FRAGMENT_CACHE_MB"); ok {
		cacheMB, err = strconv.Atoi(raw)
		if err != nil || cacheMB < 0 || cacheMB > 1<<20 {
			return nil, fmt.Errorf("invalid CAMPFIRE_FRAGMENT_CACHE_MB %q", raw)
		}
	}
	recordedMB := 32
	if raw, ok := os.LookupEnv("CAMPFIRE_RECORDED_CACHE_MB"); ok {
		recordedMB, err = strconv.Atoi(raw)
		if err != nil || recordedMB < 0 || recordedMB > 1<<20 {
			return nil, fmt.Errorf("invalid CAMPFIRE_RECORDED_CACHE_MB %q", raw)
		}
	}
	refsMB := messageRefsDefaultMB
	if raw, ok := os.LookupEnv("CAMPFIRE_MESSAGE_REFS_CACHE_MB"); ok {
		refsMB, err = strconv.Atoi(raw)
		if err != nil || refsMB < 0 || refsMB > 1<<20 {
			return nil, fmt.Errorf("invalid CAMPFIRE_MESSAGE_REFS_CACHE_MB %q", raw)
		}
	}
	slog.Info("message reference cache", "enabled", refsMB > 0, "cache_mib", refsMB)
	// CAMPFIRE_RECORDED_PIECES is the A/B and rollback switch for the piece
	// path; on/true (or unset) keeps it on, off/false/0 disables it. An
	// unrecognised value warns and keeps the default so a typo cannot silently
	// change serving.
	recordedPieces := true
	if raw, ok := os.LookupEnv("CAMPFIRE_RECORDED_PIECES"); ok {
		var valid bool
		recordedPieces, valid = parseRecordedPieces(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_RECORDED_PIECES; keeping pieces on", "value", raw)
		}
	}
	searchCache, err := openSearchCache()
	if err != nil {
		return nil, err
	}
	slog.Info("recorded response pieces", "enabled", recordedPieces, "cache_mib", recordedMB)
	// CAMPFIRE_FAST_RENDER compiles the message-uncached fragment once at
	// startup into literal/field ops (internal/web/fastrender.go). off (or
	// false/0) reverts messageViews to html/template execution, byte-
	// identically.
	fastRender := true
	if raw, ok := os.LookupEnv("CAMPFIRE_FAST_RENDER"); ok {
		var valid bool
		fastRender, valid = parseFastRender(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_FAST_RENDER; keeping fast render on", "value", raw)
		}
	}
	var renderer *messageRenderer
	if fastRender {
		// The compiler parses and escapes its own private template copy so
		// the serving set stays pre-execution (Clone etc. keep working).
		renderer, err = compileMessageRenderer(secrets, avatars)
		if err != nil {
			renderer = nil
			slog.Warn("fastrender compile failed; message fragments fall back to html/template", "error", err)
		}
	}
	slog.Info("message fragment renderer", "compiled", renderer != nil)
	// CAMPFIRE_AUTH_FAST is the A/B and rollback switch for the auth/session
	// fast path; on/true (or unset) keeps it on, off/false/0 restores the
	// per-request full cookie verification and the two-step session read.
	authFast := true
	if raw, ok := os.LookupEnv("CAMPFIRE_AUTH_FAST"); ok {
		var valid bool
		authFast, valid = parseAuthFast(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_AUTH_FAST; keeping auth fast path on", "value", raw)
		}
	}
	var authCache *verifiedCookieCache
	if authFast {
		authCache = newVerifiedCookieCache()
	}
	slog.Info("auth fast path", "enabled", authFast)
	// CAMPFIRE_RECORDED_GZIP_LEVEL (ENGINE-50): the gzip level cached members
	// are compressed at on fill, 9 by default; 6 is the pre-engine level and
	// the A/B switch. Compression happens once per piece, so the level is
	// never on the request path.
	gzipLevel := 9
	if raw, ok := os.LookupEnv("CAMPFIRE_RECORDED_GZIP_LEVEL"); ok {
		level, err := strconv.Atoi(raw)
		if err != nil || level < 1 || level > 9 {
			slog.Warn("invalid CAMPFIRE_RECORDED_GZIP_LEVEL; keeping level 9", "value", raw)
		} else {
			gzipLevel = level
		}
	}
	setRecordedGzipFillLevel(gzipLevel)
	slog.Info("recorded gzip fill level", "level", gzipLevel)
	// CAMPFIRE_RECORDED_ZSTD turns the zstd frame variant on or off. It is
	// OFF by default since ENGINE-51: Chromium decodes only the first frame
	// of a multi-frame zstd stream (exactly as it decodes only the first
	// member of a multi-member gzip stream), and every piece-path page
	// assembles several pieces, so the multi-frame zstd body — which the
	// loadgen and curl decode fine — renders an empty message list in
	// browsers. When the flag is on, multi-piece responses fall back to the
	// single-member gzip splice and only single-frame shapes use zstd; the
	// implementation stays for non-browser clients and for the corpus that
	// exercises it. gzip serves the same bytes on or off, so an off default
	// cannot change what a gzip/identity client receives.
	zstdPieces := false
	if raw, ok := os.LookupEnv("CAMPFIRE_RECORDED_ZSTD"); ok {
		var valid bool
		zstdPieces, valid = parseRecordedPieces(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_RECORDED_ZSTD; keeping zstd off", "value", raw)
		}
	}
	slog.Info("recorded zstd members", "enabled", zstdPieces)
	// CAMPFIRE_PRECOMPOSED_FRAMING turns the ENGINE-49 head/date framing on
	// (default) or off; off is the http.Header map path, byte-identical.
	precomposed := true
	if raw, ok := os.LookupEnv("CAMPFIRE_PRECOMPOSED_FRAMING"); ok {
		var valid bool
		precomposed, valid = parseRecordedPieces(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_PRECOMPOSED_FRAMING; keeping framing on", "value", raw)
		}
	}
	slog.Info("recorded precomposed framing", "enabled", precomposed)
	// CAMPFIRE_REQUEST_ARENA turns the ENGINE-48 per-request arena on
	// (default) or off; off keeps the pooled/fresh allocations, byte-identical.
	arenaOn := true
	if raw, ok := os.LookupEnv("CAMPFIRE_REQUEST_ARENA"); ok {
		var valid bool
		arenaOn, valid = parseRecordedPieces(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_REQUEST_ARENA; keeping arena on", "value", raw)
		}
	}
	slog.Info("request arena", "enabled", arenaOn)
	// CAMPFIRE_READ_CACHE turns the ENGINE-43/44 read caches on (default) or
	// off; off is the uncached fastdb/database/sql reads, byte-identical.
	readCache := newReadCache(8 << 20)
	if raw, ok := os.LookupEnv("CAMPFIRE_READ_CACHE"); ok {
		var valid bool
		enabled, valid := parseRecordedPieces(raw)
		if !valid {
			slog.Warn("invalid CAMPFIRE_READ_CACHE; keeping read cache on", "value", raw)
		} else if !enabled {
			readCache = nil
		}
	}
	s := &Server{fragments: newFragmentCache(cacheMB << 20), refsCache: newMessageRefsCache(refsMB << 20), pieces: piececache.New(recordedMB << 20), recordedPieces: recordedPieces, zstdPieces: zstdPieces, precomposed: precomposed, arenaOn: arenaOn, xVersion: appVersion(), xRev: revision(), readCache: readCache, searchCache: searchCache, fastdb: openFastPool(dbPath), fastRender: renderer, authFast: authFast, authCache: authCache, avatars: avatars, Cable: cable.New(db, secrets), DB: db, Secrets: secrets, Secure: secure, mux: &router{}, templates: t, attempts: map[string]attempt{}, dummyHash: hash}
	storageRoot := "storage"
	if len(storagePaths) > 0 {
		storageRoot = storagePaths[0]
	}
	s.Storage = storage.New(db, secrets, storageRoot)
	s.DB.ResetConnections = s.Cable.Reconnect
	s.registerStorageRoutes()
	s.Unfurler = integrations.NewUnfurler()
	s.Webhooks = integrations.NewWebhookClient()
	s.initJobs()
	s.mux.HandleFunc("POST /unfurl_link", s.auth(s.unfurl))
	s.registerPWARoutes()
	s.mux.HandleFunc("GET /qr_code/{code}", s.browserCheck(s.qrCode))
	s.mux.HandleFunc("GET /autocompletable/users", s.auth(s.autocomplete))
	s.mux.HandleFunc("GET /autocompletable/users.json", s.auth(s.autocomplete))
	s.mux.HandleFunc("GET /cable", s.auth(s.serveCable))
	s.mux.HandleFunc("GET /up", s.health)
	s.mux.HandleFunc("GET /up.json", s.health)
	s.mux.HandleFunc("GET /session/new", s.browserCheck(s.loginForm))
	s.mux.HandleFunc("POST /session", s.browserCheck(s.login))
	s.mux.HandleFunc("DELETE /session", s.auth(s.logout))
	s.mux.HandleFunc("GET /first_run", s.browserCheck(s.setupForm))
	s.mux.HandleFunc("POST /first_run", s.browserCheck(s.setup))
	s.mux.HandleFunc("GET /{$}", s.auth(s.home))
	s.mux.HandleFunc("GET /rooms", s.auth(s.home))
	s.mux.HandleFunc("GET /rooms/{id}", s.auth(s.room))
	s.mux.HandleFunc("GET /rooms/{id}/messages", s.auth(s.messages))
	s.mux.HandleFunc("POST /rooms/{id}/messages", s.auth(s.createMessage))
	s.mux.HandleFunc("GET /users/{user}/sidebar", s.auth(s.sidebar))
	s.mux.HandleFunc("GET /users/sidebar", s.auth(s.sidebar))
	s.registerMessageRoutes()
	s.registerRoomRoutes()
	s.registerMediaRoutes()
	s.registerAccountRoutes()
	s.mux.HandleFunc("GET /searches", s.auth(s.search))
	s.mux.HandleFunc("POST /searches", s.auth(s.search))
	s.mux.HandleFunc("DELETE /searches/clear", s.auth(s.search))
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(context.WithValue(r.Context(), requestHostKey{}, r.Host))
	r = r.WithContext(context.WithValue(r.Context(), requestOriginKey{}, s.origin(r)))
	if assets.Serve(w, r) {
		return
	}
	if r.URL.Path != "/cable" && !strings.HasPrefix(r.URL.Path, "/rails/active_storage/") {
		// ENGINE-48: the response buffer, its parts and the precomposed head
		// and body come from one per-request reclaimed block (reset, not
		// freed). The buffer struct itself is pooled; finish runs first, the
		// buffer is returned to its pool, and the block is released last, so
		// no carve is read after release.
		var arena *requestArena
		if s.arenaOn {
			arena = borrowRequestArena()
		}
		defer releaseRequestArena(arena)
		buffered := borrowResponseBuffer(w, arena)
		// ENGINE-49 writer gate: when the chain ends in a writer that takes
		// precomposed responses and the flags are on, the fixed security and
		// recorded headers skip the http.Header map entirely (they live in
		// the head block); a request that later falls back to the map path
		// restores them from the captured process constants.
		buffered.framed = s.precomposed && s.arenaOn && findPrecomposedReceiver(w) != nil
		w = buffered
		defer releaseResponseBuffer(buffered)
		defer func() { buffered.finish(r) }()
	}
	w, r = s.withBrowserSession(w, r)
	state := browserState(r)
	defer releaseBrowserSession(state)
	defer releaseSessionWriter(w.(*sessionWriter))
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
		// The process-constant version headers are the one ServeHTTP header
		// pair that varies by env at startup; a framed request carries them
		// in the head block instead of the map.
		if buffered, ok := w.(*responseBuffer); !ok || !buffered.framed {
			w.Header().Set("X-Version", s.xVersion)
			w.Header().Set("X-Rev", s.xRev)
		}
	}
	if buffered, ok := w.(*responseBuffer); !ok || !buffered.framed {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-XSS-Protection", "0")
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
	}
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
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, status int, p page) {
	if name != "incompatible-browser" && respondFormat(w, r, "html") == "" {
		return
	}
	a, err := s.accountCached(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	notice, alert := s.consumeFlash(r)
	p.Notice = notice
	if p.Error == "" {
		p.Error = alert
	}
	p.Account = a
	p.Email = r.Form.Get("email_address")
	if name == "login" || name == "join" {
		p.Reload = true
		users, e := s.DB.Users(r.Context(), 0, false)
		if e == nil {
			for _, u := range users {
				if u.Role == 1 && (p.HelpContact.ID == 0 || u.ID < p.HelpContact.ID) {
					p.HelpContact = u
				}
			}
		}
	}
	p.BackPath = "/"
	if name == "room-form" || name == "account" || name == "push-subscriptions" {
		if id, e := s.lastRoom(r, p.User.ID); e == nil {
			p.BackPath = fmt.Sprintf("/rooms/%d", id)
		}
	}
	p.Version = appVersion()
	if r.Header.Get("Turbo-Frame") != "" && name != "edit-message" && name != "show-message" && name != "incompatible-browser" && name != "room-not-found" {
		p.Frame = true
	}
	p.Platform = useragent.Parse(r.UserAgent()).View()
	p.Screen = name
	p.Chat = name == "room" && p.Room.ID != 0
	if s.Push.VAPID != nil {
		p.VAPIDPublicKey = s.Push.VAPID.PublicKey()
	}
	p.LoadedAt = strconv.FormatInt(s.DB.Now().UnixMilli(), 10)
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
	var recorded *recordedPayload
	var raw []database.Message
	encoding := "identity"
	if len(p.Messages) > 0 {
		raw = make([]database.Message, len(p.Messages))
		for i, m := range p.Messages {
			raw[i] = m.Message
		}
		if name == "room" || name == "messages" || name == "search" {
			// One negotiation per request: recordedMessageList needs it for
			// storage, writeRecordedPieces for the response form.
			encoding = s.clientEncoding(r)
			payload, listErr := s.recordedMessageList(r.Context(), raw, encoding == "gzip", s.zstdPieces && encoding == "zstd")
			if listErr != nil {
				s.fail(w, listErr)
				return
			}
			recorded = &payload
			if !s.recordedPieces {
				p.MessagesHTML = recordedMessageMarker()
			}
		} else {
			p.Messages, err = s.messageViews(r.Context(), raw)
			if err == nil && name == "edit-message" {
				for i := range p.Messages {
					p.Messages[i].Editable, _ = richtext.Editable(p.Messages[i].Body, s.richContext(r.Context()))
				}
			}
		}
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if name == "search" {
		p.ReturnRoom, _ = s.lastRoom(r, p.User.ID)
	}
	if s.recordedPieces && recorded != nil {
		// A framed request carries Content-Type in the head block; the map
		// path needs it set here as always.
		if buffered := findResponseBuffer(w); buffered == nil || !buffered.framed {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		handled, renderErr := s.writeRecordedPieces(w, r, status, name, p, *recorded, encoding)
		if renderErr != nil {
			s.fail(w, renderErr)
			return
		}
		if handled {
			return
		}
		// The shell could not be split into stable pieces (an unexpected
		// template shape). The piece payload carries no legacy fragment, so
		// render the list through the legacy path for this request; the next
		// request repeats the attempt and falls back the same way.
		p.MessagesHTML = recordedMessageMarker()
		fragment, listErr := s.messageList(r.Context(), raw)
		if listErr != nil {
			s.fail(w, listErr)
			return
		}
		recorded = &recordedPayload{fragment: fragment}
	}
	if name == "room" && recorded != nil {
		shell, marker, err := s.roomShell(p)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeRecorded(w, status, shell, marker, recorded.fragment)
		return
	}
	sidebarKey := ""
	if name == "sidebar" {
		sidebarKey = p.sidebarKey
		if sidebarKey != "" {
			if fragment, ok := s.fragments.get(sidebarKey); ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(status)
				w.Write([]byte(fragment))
				return
			}
		}
	}
	b := borrowBuffer()
	defer releaseBuffer(b)
	if err := s.templates.ExecuteTemplate(b, name, p); err != nil {
		s.fail(w, err)
		return
	}
	if sidebarKey != "" {
		s.fragments.put(sidebarKey, template.HTML(b.String()))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if recorded != nil {
		writeRecorded(w, status, b.String(), string(p.MessagesHTML), recorded.fragment)
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
func (s *Server) auth(next func(http.ResponseWriter, *http.Request, database.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := s.DB.Now()
		var token string
		c, err := r.Cookie("session_token")
		if err == nil {
			token, err = s.verifiedSessionToken(rails.UnescapeCookie(c.Value), now)
		}
		if err != nil || token == "" {
			s.requestAuthentication(w, r)
			return
		}
		fc, release := s.fastConn(r)
		u, lastActive, err := s.sessionState(fc, r.Context(), token)
		release()
		if errors.Is(err, sql.ErrNoRows) {
			s.requestAuthentication(w, r)
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		// The hourly refresh: the joined read supplies last_active_at, so the
		// RefreshSession writer (and the re-signed cookie) runs only when the
		// session is due — the same gate RefreshSession's own SELECT applies
		// on the legacy path, which reports a zero lastActive and therefore
		// refreshes on every request exactly as before.
		if lastActive.IsZero() || lastActive.Before(now.Add(-time.Hour)) {
			refreshed, err := s.DB.RefreshSession(r.Context(), token, r.UserAgent(), remoteIP(r))
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
		}
		if s.blockBrowser(w, r) {
			return
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
	if !s.requireUnauthenticated(w, r) {
		return
	}
	exists, err := s.hasAccount(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if !exists {
		http.Redirect(w, r, "/first_run", 302)
		return
	}
	s.render(w, r, "login", 200, page{Title: "Sign in"})
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
	s.render(w, r, "first-run", 200, page{Title: "Set up Campfire", Setup: true})
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
	if !s.requireUnauthenticated(w, r) {
		return
	}
	if !s.allowLogin(remoteIP(r)) {
		s.render(w, r, "login", 429, page{Title: "Sign in", Error: "Too many requests or unauthorized."})
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
		s.render(w, r, "login", 401, page{Title: "Sign in", Error: "Too many requests or unauthorized."})
		return
	}
	s.startSession(w, r, u)
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	password := r.Form.Get("user[password]")
	if password == "" || len(password) > 72 {
		s.render(w, r, "first-run", 422, page{Title: "Set up Campfire", Setup: true, Error: "Password must contain 1 to 72 bytes."})
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
	u, err := s.DB.Setup(r.Context(), r.Form.Get("user[name]"), r.Form.Get("user[email_address]"), string(digest), pendingBlob(upload))
	if errors.Is(err, database.ErrForbidden) {
		http.Redirect(w, r, "/", 302)
		return
	}
	if errors.Is(err, database.ErrValidation) {
		s.render(w, r, "first-run", 422, page{Title: "Set up Campfire", Setup: true, Error: "Name and email address are required."})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.analyzeUpload(upload)
	s.startSession(w, r, u)
}
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u database.User) {
	token, err := s.DB.StartSession(r.Context(), u.ID, r.UserAgent(), remoteIP(r))
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
	token, err := s.verifiedSessionToken(rails.UnescapeCookie(c.Value), s.DB.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.DB.DeleteSession(r.Context(), token, u.ID); err != nil {
		s.fail(w, err)
		return
	}
	if s.authCache != nil {
		// The revoked token must not be served from the verification cache;
		// the per-request session read would reject it either way.
		s.authCache.remove(rails.UnescapeCookie(c.Value))
	}
	s.Cable.Disconnect(u.ID)
	if endpoint := r.Form.Get("push_subscription_endpoint"); endpoint != "" {
		if _, err := s.DB.Write.ExecContext(r.Context(), "DELETE FROM push_subscriptions WHERE user_id=? AND endpoint=?", u.ID, endpoint); err != nil {
			s.fail(w, err)
			return
		}
	}
	browserState(r).reset()
	http.SetCookie(w, &http.Cookie{Name: "session_token", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", 302)
}
func (s *Server) lastRoom(r *http.Request, user int64) (int64, error) {
	if cookie, err := r.Cookie("last_room"); err == nil {
		if id, err := strconv.ParseInt(cookie.Value, 10, 64); err == nil {
			if _, err = s.DB.Room(r.Context(), user, id); err == nil {
				return id, nil
			}
		}
	}
	return s.originalRoomCached(r.Context(), user)
}
func (s *Server) home(w http.ResponseWriter, r *http.Request, u database.User) {
	id, err := s.lastRoom(r, u.ID)
	if errors.Is(err, sql.ErrNoRows) {
		s.render(w, r, "welcome", 200, page{Title: "No rooms yet", User: u})
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
		value = r.PathValue("id")
	}
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}
func viewMessages(messages []database.Message) []messageView {
	result := make([]messageView, 0, len(messages))
	for _, m := range messages {
		result = append(result, messageView{Message: m})
	}
	return result
}
func (s *Server) room(w http.ResponseWriter, r *http.Request, u database.User) {
	room, messages, view, invitation, err := s.roomData(r, u)
	if err != nil {
		s.roomLookupFailure(w, r, err)
		return
	}
	room = view.Room
	s.rememberRoom(w, r, strconv.FormatInt(room.ID, 10))
	s.render(w, r, "room", 200, page{Invitation: invitation, Stream: s.Secrets.SignStream(rails.RoomStream(room.Type, room.ID)), Title: room.Name, User: u, Room: room, Messages: viewMessages(messages)})
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request, u database.User) {
	messages, validator, err := s.messageData(r, u)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(messages) == 0 {
		w.WriteHeader(204)
		return
	}
	if validator.apply(w, r) {
		return
	}
	s.render(w, r, "messages", 200, page{Messages: viewMessages(messages)})
}
func (s *Server) createMessage(w http.ResponseWriter, r *http.Request, u database.User) {
	if !requireMessage(w, r) {
		return
	}
	// The room is the membership check; the same record serves the stream
	// target below, so the message is never followed by a second room read.
	room, err := s.DB.Room(r.Context(), u.ID, roomID(r))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.render(w, r, "room-not-found", 200, page{User: u})
			return
		}
		s.fail(w, err)
		return
	}
	var staged *storage.Staged
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
	// One rich-text parse per posted body (ENGINE-45b): canonicalization plus
	// the plain text the store writes, the presentation the view renders and
	// the mentioned ids the push and webhook paths consume all derive from a
	// single parse of the canonical body (previously canonicalMessage, Display
	// and MentionIDs each parsed it again with their own context). The three
	// separate callers get the same values they computed themselves before;
	// the richtext oracle pins the equality.
	richCtx := s.richContext(r.Context())
	var rich *richPost
	if body != nil {
		rich = s.richPostFor(*body, richCtx)
	}
	m, err := s.saveNewMessageRich(r.Context(), u.ID, roomID(r), r.Form.Get("message[client_message_id]"), body, staged, false, rich)
	if err != nil {
		s.fail(w, err)
		return
	}

	b := borrowBuffer()
	defer releaseBuffer(b)
	// The new message's view comes from data this request already holds —
	// creator, room, body, and the empty boosts and attachment a brand-new
	// id cannot reference (AUTOINCREMENT ids are never reused) — instead of
	// messageViews' per-view reads. Attachment posts keep the messageViews
	// path, which lifts the blob row; direct rooms keep it too because their
	// display name needs the other member.
	views := make([]messageView, 0, 1)
	if staged == nil && room.Type != "Rooms::Direct" {
		view, err := s.freshMessageView(r, u, m, room, rich)
		if err != nil {
			s.fail(w, err)
			return
		}
		views = append(views, view)
	} else {
		views, err = s.messageViews(r.Context(), []database.Message{m})
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	// The append frame is assembled in the pooled buffer from the fragment
	// alone: executing the "messages" template with one view writes exactly
	// the view's fragment, and the same bytes feed both the broadcast and
	// the response, so no second render or intermediate copy happens.
	b.WriteString(`<turbo-stream action="append" target="`)
	b.WriteString(html.EscapeString(room.DOM("messages")))
	b.WriteString(`"><template>`)
	for i := range views {
		b.WriteString(string(views[i].Fragment))
	}
	b.WriteString(`</template></turbo-stream>`)
	markup := b.String()
	// Delivery follows commit and outlives a disconnected posting request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	s.Cable.Publish(ctx, m.RoomID, markup)
	cancel()
	s.messageCreated(m, room, richMentions(rich))
	s.enqueueWebhooks(m, room, richMentions(rich))
	if respondFormat(w, r, "turbo_stream") != "" {
		writeStream(w, markup)
	}
}
func (s *Server) sidebar(w http.ResponseWriter, r *http.Request, u database.User) {
	// The fragment key comes from the sidebar version registry alone; a hit
	// is served before any room, membership or placeholder read and before
	// render's pageSetup (account read, flash, negotiation bookkeeping).
	version, err := s.DB.SidebarVersion(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	key := sidebarFragmentKey(u.ID, version)
	if fragment, ok := s.fragments.get(key); ok {
		if respondFormat(w, r, "html") == "" {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		w.Write([]byte(fragment))
		return
	}
	items, placeholders, err := s.sidebarData(r, u)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "sidebar", 200, page{Placeholders: placeholders, SidebarRooms: items, User: u, RoomsStream: s.Secrets.SignStream("rooms"), UserRoomsStream: s.Secrets.SignStream(rails.UserRoomsStream(u.ID)), sidebarKey: key})
}
func (s *Server) search(w http.ResponseWriter, r *http.Request, u database.User) {
	q := database.SearchQuery(r.FormValue("q"))
	if r.Method == "POST" {
		if err := s.DB.RecordSearch(r.Context(), u.ID, q); err != nil {
			s.fail(w, err)
			return
		}
		// Recording a search changes the recent list the page renders; the
		// result cache carries no recent-list version, so the user's entries
		// are purged and the next GET of any query re-reads.
		if s.searchCache != nil {
			s.searchCache.purgeUser(u.ID)
		}
		http.Redirect(w, r, "/searches?q="+url.QueryEscape(q), 302)
		return
	}
	if r.Method == "DELETE" {
		if _, err := s.DB.Write.ExecContext(r.Context(), "DELETE FROM searches WHERE user_id=?", u.ID); err != nil {
			s.fail(w, err)
			return
		}
		if s.searchCache != nil {
			s.searchCache.purgeUser(u.ID)
		}
		http.Redirect(w, r, "/searches", 302)
		return
	}
	if s.searchCache != nil {
		if result, ok := s.searchCache.get(u.ID, q, s.DB.CorpusVersion(), s.DB.MembershipVersion()); ok {
			// Hit: the message fragments come from the recorded piece cache
			// (keyed by exactly these messages' id/stamp pairs), so no Search,
			// Rooms or RecentSearches read happens and nothing renders the
			// fragments again.
			s.render(w, r, "search", 200, page{Title: "Search", Query: q, User: u, Messages: viewMessages(result.messages), RecentSearches: result.recent})
			return
		}
	}
	recent, err := s.DB.RecentSearches(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	fc, release := s.fastConn(r)
	messages, err := s.searchResults(fc, r.Context(), u.ID, q)
	release()
	if err != nil {
		s.fail(w, err)
		return
	}
	rooms, err := s.DB.Rooms(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.searchCache != nil {
		// Store only when the versions are still the ones the reads saw: a
		// write that committed mid-request must not tag fresh rows with a
		// stale version (or vice versa). A dropped store is just a miss.
		corpus := s.DB.CorpusVersion()
		membership := s.DB.MembershipVersion()
		if s.DB.CorpusVersion() == corpus && s.DB.MembershipVersion() == membership {
			s.searchCache.put(u.ID, q, corpus, membership, searchResult{recent: recent, messages: messages})
		}
	}
	s.render(w, r, "search", 200, page{Title: "Search", Query: q, User: u, Rooms: rooms, Messages: viewMessages(messages), RecentSearches: recent})
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
	token, err := s.verifiedSessionToken(rails.UnescapeCookie(c.Value), s.DB.Now())
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	s.Cable.Serve(w, r, u, token)
}
func (s *Server) Close() {
	if s.fastdb != nil {
		s.fastdb.Close()
	}
	s.Jobs.Close(10 * time.Second)
	s.Cable.Close()
}

func appVersion() string {
	for _, key := range []string{"APP_VERSION", "GIT_REVISION"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return "Go"
}

// revision is the X-Rev header value, captured once at startup so the
// precomposed head block can embed it.
func revision() string {
	if value := os.Getenv("GIT_REVISION"); value != "" {
		return value
	}
	return "0"
}
