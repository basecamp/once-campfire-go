package web

import (
	"net/http"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

func (s *Server) browserCheck(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.blockBrowser(w, r) {
			next(w, r)
		}
	}
}

// ApplicationController's allow_browser runs after authentication and forgery protection.
func (s *Server) blockBrowser(w http.ResponseWriter, r *http.Request) bool {
	return s.blockBrowserFor(w, r, nil)
}

// blockBrowserFor is allow_browser with Current.user as authentication left it.
func (s *Server) blockBrowserFor(w http.ResponseWriter, r *http.Request, user *database.User) bool {
	blocked, _ := useragent.Parse(r.UserAgent()).Blocked()
	if !blocked {
		return false
	}
	s.renderIncompatibleBrowser(w, r, user)
	return true
}
