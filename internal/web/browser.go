package web

import (
	"net/http"
	"strings"
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
	blocked, _ := requestAgent(r).Blocked()
	if !blocked {
		return false
	}
	frame := r.Header.Get("Turbo-Frame") != ""
	if route, _, _ := recognizeRequest(r); route != nil {
		if strings.HasPrefix(route.Endpoint, "messages#") ||
			strings.HasPrefix(route.Endpoint, "messages/by_bots#") {
			frame = false
		}
	}
	s.respondPage(w, r, "incompatible-browser", http.StatusOK, page{Frame: frame})
	return true
}
