package web

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func (s *Server) registerMediaRoutes() {
	s.mux.HandleFunc("GET /users/{token}/avatar", liveResponse(s.auth(s.avatarShow)))
	s.mux.HandleFunc("DELETE /users/{user}/avatar", s.auth(s.deleteAvatar))
	s.mux.HandleFunc("GET /account/logo", s.browserCheck(s.logo))
	s.mux.HandleFunc("DELETE /account/logo", s.auth(s.deleteLogo))
}
func (s *Server) serveVariant(w http.ResponseWriter, r *http.Request, kind string, id int64, name string, size int64, format string) bool {
	b, err := s.Storage.Attached(r.Context(), kind, id, name)
	if errors.Is(err, database.ErrNoRows) {
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
	s.serveStored(w, r, path, b.Type(), storage.Disposition("inline", storage.Filename(b.Filename)), true)
	return true
}
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name, ct string) {
	data, err := fs.ReadFile(assets.Public(), strings.TrimPrefix(assets.Path(name), "/"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", storage.Disposition("inline", name[strings.LastIndex(name, "/")+1:]))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
func (s *Server) logo(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Account(r.Context())
	if err != nil && !errors.Is(err, database.ErrNoRows) {
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
	if a.ID != 0 && s.serveVariant(w, r, "Account", a.ID, "logo", size, "png") {
		return
	}
	s.serveAsset(w, r, asset, "image/png")
}
func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request, u database.User) {
	if err := s.Storage.Detach(r.Context(), "User", u.ID, "avatar"); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/users/me/profile", 302)
}
func (s *Server) deleteLogo(w http.ResponseWriter, r *http.Request, u database.User) {
	if !administrator(w, u) {
		return
	}
	a, err := s.DB.Account(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if err = s.Storage.Detach(r.Context(), "Account", a.ID, "logo"); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/account/edit", 302)
}
