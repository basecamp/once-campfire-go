package web

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"hash/crc32"
	"html"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func (s *Server) avatar(w http.ResponseWriter, r *http.Request, _ database.User) {
	id, err := s.Secrets.VerifyID("User", r.PathValue("user_id"), "avatar", s.DB.Now())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	user, err := s.DB.User(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "max-age=1800, public, stale-while-revalidate=604800")
	if mediaNotModified(w, r, "users", user.ID, user.UpdatedAt) {
		return
	}
	if s.serveVariant(w, r, "User", id, "avatar", 512, "webp") {
		return
	}
	if user.Role == 2 {
		s.serveAsset(w, r, "default-bot-avatar.svg", "image/svg+xml")
		return
	}
	initials := []rune{}
	boundary := true
	for _, c := range user.Name {
		if boundary &&
			(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			initials = append(initials, c)
		}
		boundary = !(unicode.IsLetter(c) || unicode.IsNumber(c) || c == '_')
	}
	colors := []string{
		"#AF2E1B",
		"#CC6324",
		"#3B4B59",
		"#BFA07A",
		"#ED8008",
		"#ED3F1C",
		"#BF1B1B",
		"#736B1E",
		"#D07B53",
		"#736356",
		"#AD1D1D",
		"#BF7C2A",
		"#C09C6F",
		"#698F9C",
		"#7C956B",
		"#5D618F",
		"#3B3633",
		"#67695E",
	}
	color := colors[int(crc32.ChecksumIEEE([]byte(strconv.FormatInt(id, 10))))%len(colors)]
	fit := ""
	if len(initials) >= 3 {
		fit = `textLength="85%" lengthAdjust="spacingAndGlyphs"`
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	fmt.Fprintf(
		w,
		`<svg version="1.1" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
  viewBox="0 0 512 512" class="avatar" aria-hidden="true">
  <defs>
    <clipPath id="porthole">
      <circle cx="50%%" cy="50%%" r="50%%" />
    </clipPath>
  </defs>

  <g>
    <rect width="100%%" height="100%%" rx="50" fill="%s" />

    <text x="50%%" y="50%%" fill="#FFFFFF"
      text-anchor="middle" dy="0.35em"
      %s
      font-family="-apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Helvetica, Arial, sans-serif"
      font-size="230"
      font-weight="800"
      letter-spacing="-5">
      %s
    </text>
  </g>
</svg>

`,
		color,
		fit,
		html.EscapeString(string(initials)),
	)
}

// Media freshness belongs to the record, before selecting a streamed file or
// rendering initials. File responses bypass the completed-body digest emitter.
func mediaNotModified(w http.ResponseWriter, r *http.Request, kind string, id int64, updated time.Time) bool {
	etag := fmt.Sprintf("W/\"%s-%d-%x\"", kind, id, updated.UnixNano())
	w.Header().Set("ETag", etag)
	return notModified(w, r, etag, time.Time{})
}

func (s *Server) serveVariant(
	w http.ResponseWriter,
	r *http.Request,
	kind string,
	id int64,
	name string,
	size int64,
	format string,
) bool {
	b, err := s.DB.AttachedBlob(r.Context(), kind, id, name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		s.fail(w, err)
		return true
	}
	if !storage.Variable(b.Type()) {
		return false
	}
	b, err = s.Storage.Variant(r.Context(), b, storage.Resize(size, size, format))
	if err != nil {
		s.fail(w, err)
		return true
	}
	path, err := s.Storage.Path(b.Key)
	if err != nil {
		s.fail(w, err)
		return true
	}
	s.serveStored(
		w,
		r,
		path,
		b.Type(),
		storage.Disposition("inline", storage.Filename(b.Filename)),
		contentFile,
	)
	return true
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name, ct string) {
	data, err := fs.ReadFile(assets.Public(), strings.TrimPrefix(assets.Path(name), "/"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().
		Set("Content-Disposition", storage.Disposition("inline", name[strings.LastIndex(name, "/")+1:]))
	streamFileResponse(w)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (s *Server) logo(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Account(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	size := int64(512)
	asset := "logos/app-icon.png"
	if r.URL.Query().Get("size") == "small" {
		size = 192
		asset = "logos/app-icon-192.png"
	}
	w.Header().Set("Cache-Control", "max-age=300, public, stale-while-revalidate=604800")
	if a.ID != 0 && mediaNotModified(w, r, "accounts", a.ID, a.UpdatedAt) {
		return
	}
	if a.ID != 0 && s.serveVariant(w, r, "Account", a.ID, "logo", size, "png") {
		return
	}
	s.serveAsset(w, r, asset, "image/png")
}

func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request, u database.User) {
	result, err := s.AccountCommands.DeleteAvatar(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
	http.Redirect(w, r, "/users/me/profile", 302)
}

func (s *Server) deleteLogo(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	result, err := s.AccountCommands.DeleteLogo(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	logAccountProcessing(result.Processing)
	http.Redirect(w, r, "/account/edit", 302)
}
