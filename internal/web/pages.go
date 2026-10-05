package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/useragent"
	"github.com/basecamp/once-campfire-go/internal/views"
	qt "github.com/valyala/quicktemplate"
)

// Pages in the application layout, as the reference renders them
// (reference/crates/campfire/src/controllers/presenters/{view_context,page}.rs): load what the
// layout needs, render the page with a ViewContext for this request into a recorded page, and
// hand it to the response, whose ETag comes from the page's parts.

// layout is everything the application layout needs, loaded before rendering (Layout::load).
type layout struct {
	currentUser       *views.CurrentUser
	account           views.AccountSummary
	customStyles      *string
	platform          views.Platform
	lastRoomVisitedID *int64
}

// loadLayout is Layout::load: Current.account, Current.user, last_room_visited and the platform.
// With no account yet (first run) the account summary is blank.
func (s *Server) loadLayout(r *http.Request, user *database.User) (*layout, error) {
	ctx := r.Context()
	account, found, err := s.DB.AccountFirst(ctx)
	if err != nil {
		return nil, err
	}
	l := &layout{platform: viewPlatform(useragent.Parse(r.UserAgent()).View())}
	if found {
		_, hasLogo, err := s.DB.AttachedBlob(ctx, "Account", account.ID, "logo")
		if err != nil {
			return nil, err
		}
		l.account = s.accountSummary(&account, hasLogo)
		if account.HasCustomStyles {
			l.customStyles = &account.CustomStyles
		}
	} else {
		l.account = s.accountSummary(nil, false)
	}
	if user != nil {
		l.currentUser = s.currentUser(user)
		room, found, err := s.lastRoomVisitedIn(ctx, user.ID, lastRoomCookie(r))
		if err != nil {
			return nil, err
		}
		if found {
			l.lastRoomVisitedID = &room.ID
		}
	}
	return l, nil
}

// lastRoomVisitedIn is last_room_visited: the last_room cookie's room if the user is in it, else
// Current.user.rooms.original.
func (s *Server) lastRoomVisitedIn(ctx context.Context, user int64, lastRoom *int64) (database.ReferenceRoom, bool, error) {
	if lastRoom != nil {
		room, found, err := s.DB.RoomForUser(ctx, user, *lastRoom)
		if err != nil || found {
			return room, found, err
		}
	}
	return s.DB.OriginalRoomForUser(ctx, user)
}

func lastRoomCookie(r *http.Request) *int64 {
	cookie, err := r.Cookie("last_room")
	if err != nil {
		return nil
	}
	id, ok := integerCast(cookie.Value)
	if !ok {
		return nil
	}
	return &id
}

// toFSNumber is `time.to_fs(:number)`.
func toFSNumber(t time.Time) string { return t.UTC().Format("20060102150405") }

// avatarPath is fresh_user_avatar_path(user).
func (s *Server) avatarPath(id int64, updatedAt time.Time) string {
	return views.RouteFreshUserAvatar(s.Secrets.SignedID("User", id, "avatar", time.Time{}), toFSNumber(updatedAt))
}

// currentUser is Current.user as the layout's meta tags and helpers see it.
func (s *Server) currentUser(u *database.User) *views.CurrentUser {
	return &views.CurrentUser{ID: u.ID, Name: u.Name, Administrator: u.Role == 1, Bot: u.Role == 2, AvatarURL: s.avatarPath(u.ID, u.UpdatedAt)}
}

// accountSummary is Current.account for the layout: its name, fresh_account_logo_path and whether
// a logo is attached.
func (s *Server) accountSummary(account *database.Account, hasLogo bool) views.AccountSummary {
	var v *string
	name := ""
	if account != nil {
		v = views.Ptr(toFSNumber(account.UpdatedAt))
		name = account.Name
	}
	return views.AccountSummary{Name: name, LogoURL: views.RouteFreshAccountLogo(v, nil), HasLogo: hasLogo}
}

func viewPlatform(p useragent.Platform) views.Platform {
	return views.Platform{IOS: p.IOS, Android: p.Android, Mac: p.Mac, Windows: p.Windows, Chrome: p.Chrome, Firefox: p.Firefox,
		Safari: p.Safari, Edge: p.Edge, Mobile: p.Mobile, Desktop: p.Desktop, AppleMessages: p.AppleMessages,
		Browser: p.Browser, OperatingSystem: p.OperatingSystem}
}

// viewContext is Layout::render's ViewContext for this request. The flash is read (and so swept
// at the end of the request) as the layout's flash[:notice] / flash[:alert] read it.
func (s *Server) viewContext(r *http.Request, l *layout) *views.ViewContext {
	notice, alert := s.consumeFlash(r)
	ctx := s.baseViewContext(s.origin(r))
	ctx.CurrentUser = l.currentUser
	ctx.Account = l.account
	if notice != "" {
		ctx.FlashNotice = &notice
	}
	if alert != "" {
		ctx.FlashAlert = &alert
	}
	ctx.Platform = l.platform
	ctx.CustomStyles = l.customStyles
	ctx.RequestURL = s.origin(r) + r.URL.RequestURI()
	if referrer := r.Referer(); referrer != "" {
		ctx.Referrer = &referrer
	}
	ctx.LastRoomVisitedID = l.lastRoomVisitedID
	return ctx
}

// baseViewContext is what every ViewContext shares, with base as the base URL.
func (s *Server) baseViewContext(base string) *views.ViewContext {
	ctx := &views.ViewContext{
		AssetPath:      assets.Path,
		ImportmapTags:  views.HTML(assets.Importmap),
		StylesheetTags: views.HTML(assets.Stylesheets),
		CableURL:       "/cable",
		BaseURL:        base,
		RequestURL:     base + "/",
		AppVersion:     appVersion(),
		Cache:          s.views,
	}
	if s.Push.VAPID != nil {
		ctx.VAPIDPublicKey = views.Ptr(s.Push.VAPID.PublicKey())
	}
	return ctx
}

// detachedViewContext is render_detached_at: the ViewContext ApplicationController.render has (no
// request, no Current.user, no CSRF tokens), with base as its base URL.
func (s *Server) detachedViewContext(ctx context.Context, base string) (*views.ViewContext, error) {
	account, found, err := s.DB.AccountFirst(ctx)
	if err != nil {
		return nil, err
	}
	v := s.baseViewContext(base)
	if found {
		v.Account = s.accountSummary(&account, false)
	} else {
		v.Account = s.accountSummary(nil, false)
	}
	return v, nil
}

// rendererBaseURL is renderer_base_url: protocol and host, without the port (SetCurrentRequest's
// default_url_options carry only the request's host and protocol).
func (s *Server) rendererBaseURL(r *http.Request) string {
	origin := s.origin(r)
	scheme, host, _ := strings.Cut(origin, "://")
	if name, _, found := strings.Cut(host, ":"); found && !strings.HasPrefix(host, "[") {
		host = name
	}
	return scheme + "://" + host
}

// pageOrFrame renders page in the application layout, or, for a Turbo-Frame request, its head and
// content blocks in turbo-rails' frame layout (page_or_frame).
func (s *Server) pageOrFrame(w http.ResponseWriter, r *http.Request, user *database.User, status int, size *views.RenderSize, build func(ctx *views.ViewContext) views.Page) {
	if !s.findTemplate(w, r) {
		return
	}
	l, err := s.loadLayout(r, user)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := s.viewContext(r, l)
	page := build(ctx)
	if r.Header.Get("Turbo-Frame") != "" {
		s.writePage(w, r, status, "text/html; charset=utf-8", false, size.Render(0, func(qw *qt.Writer) { views.StreamLayoutsTurboRailsFrame(qw, ctx, page) }))
		return
	}
	s.writePage(w, r, status, "text/html; charset=utf-8", true, size.Render(0, func(qw *qt.Writer) { views.StreamLayoutsApplication(qw, ctx, page) }))
}

// findTemplate is the implicit render's template lookup: an action whose only template is HTML
// can't answer a request that doesn't accept HTML (406).
func (s *Server) findTemplate(w http.ResponseWriter, r *http.Request) bool {
	return respondFormat(w, r, "html") != ""
}

// writePage hands a recorded page to the response: its Content-Type, and for a page in the
// application layout the Link preload header stylesheet_link_tag adds. The response's ETag comes
// from the page's parts (responseBuffer.finish).
func (s *Server) writePage(w http.ResponseWriter, r *http.Request, status int, contentType string, preload bool, page *views.RecordedPage) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	if preload {
		h.Set("Link", appendPreloadLinks(h.Get("Link"), stylesheetPreloadLinks))
	}
	target := w
	for {
		if buffered, ok := target.(*responseBuffer); ok {
			buffered.page = page
			buffered.WriteHeader(status)
			return
		}
		wrapper, ok := target.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		target = wrapper.Unwrap()
	}
	w.WriteHeader(status)
	w.Write([]byte(page.String()))
}

// stylesheetPreloadLinks are the `<href>; rel=preload; as=style; nopush` links for every stylesheet
// the layout links (Propshaft's stylesheet_link_tag :all).
var stylesheetPreloadLinks = func() []string {
	var links []string
	for _, tag := range strings.Split(string(assets.Stylesheets), "\n") {
		_, rest, ok := strings.Cut(tag, `href="`)
		if !ok {
			continue
		}
		href, _, _ := strings.Cut(rest, `"`)
		if href != "" && !strings.HasPrefix(href, "data:") {
			links = append(links, "<"+href+">; rel=preload; as=style; nopush")
		}
	}
	return links
}()

// appendPreloadLinks is send_preload_links_header: a link that would push the header past 1,000
// bytes is left out.
func appendPreloadLinks(header string, links []string) string {
	for _, link := range links {
		if len(header)+len(link) > 1000 {
			continue
		}
		if header != "" {
			header += ","
		}
		header += link
	}
	return header
}

// integerCast is how Active Record binds a string to an integer column
// (ActiveModel::Type::Integer#serialize): nothing unless it starts like a number
// (/\A\s*[+-]?\d/), then String#to_i, and nothing out of range.
func integerCast(s string) (int64, bool) {
	unsigned := strings.TrimLeft(s, " \t\n\v\f\r")
	unsigned = strings.TrimPrefix(strings.TrimPrefix(unsigned, "+"), "-")
	if unsigned == "" || unsigned[0] < '0' || unsigned[0] > '9' {
		return 0, false
	}
	return rubyToI(s)
}

// rubyToI is String#to_i: optional leading whitespace and sign, an optional 0d, then digits (an
// underscore allowed between two); false when the value is past an int64.
func rubyToI(s string) (int64, bool) {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	negative := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		negative = s[0] == '-'
		s = s[1:]
	}
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'd' || s[1] == 'D') {
		s = s[2:]
	}
	var number uint64
	previousDigit, overflow := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			if number > (1<<63)/10 {
				overflow = true
			}
			number = number*10 + uint64(c-'0')
			previousDigit = true
		} else if c == '_' && previousDigit {
			previousDigit = false
		} else {
			break
		}
	}
	if overflow || number > 1<<63 || (!negative && number == 1<<63) {
		return 0, false
	}
	if negative {
		return -int64(number), true
	}
	return int64(number), true
}
