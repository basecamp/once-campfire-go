package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

const browserSessionCookie = "_campfire_session"

type browserSessionKey struct{}
type browserSession struct {
	server          *Server
	request         *http.Request
	loaded, changed bool
	values          map[string]any
}

func (s *browserSession) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.values = map[string]any{}
	if c, err := s.request.Cookie(browserSessionCookie); err == nil {
		if err = s.server.Secrets.DecryptCookie(browserSessionCookie, rails.UnescapeCookie(c.Value), s.server.DB.Now(), &s.values); err != nil || s.values == nil {
			s.values = map[string]any{}
		}
	}
	if s.values["session_id"] == nil {
		var b [16]byte
		rand.Read(b[:])
		s.values["session_id"] = hex.EncodeToString(b[:])
	}
}
func browserState(r *http.Request) *browserSession {
	return r.Context().Value(browserSessionKey{}).(*browserSession)
}
func (s *browserSession) set(key string, value any) {
	s.load()
	if !reflect.DeepEqual(s.values[key], value) {
		s.values[key] = value
		s.changed = true
	}
}
func (s *browserSession) remove(key string) any {
	s.load()
	value, ok := s.values[key]
	if ok {
		delete(s.values, key)
		s.changed = true
	}
	return value
}
func (s *browserSession) reset() { s.loaded = true; s.values = map[string]any{}; s.changed = true }
func (s *browserSession) commit(w http.ResponseWriter) error {
	if !s.changed {
		return nil
	}
	s.changed = false
	anyValue := false
	for k, v := range s.values {
		if k != "session_id" && v != nil {
			anyValue = true
		}
	}
	if !anyValue {
		http.SetCookie(w, &http.Cookie{Name: browserSessionCookie, Path: "/", MaxAge: -1, Secure: s.server.Secure, SameSite: http.SameSiteLaxMode})
		return nil
	}
	expiry := s.server.DB.Now().AddDate(20, 0, 0)
	raw, err := s.server.Secrets.EncryptCookie(browserSessionCookie, s.values, expiry)
	if err != nil {
		return err
	}
	value := rails.EscapeCookie(raw)
	if len(browserSessionCookie)+len(value) > 4096 {
		return errors.New("session cookie overflow")
	}
	http.SetCookie(w, &http.Cookie{Name: browserSessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: s.server.Secure, SameSite: http.SameSiteLaxMode, Expires: expiry})
	return nil
}

type sessionWriter struct {
	http.ResponseWriter
	session         *browserSession
	written, failed bool
}

func (w *sessionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *sessionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if err := w.session.commit(w.ResponseWriter); err != nil {
		return nil, nil, err
	}
	connection, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.written = true
	}
	return connection, buffer, err
}

func (w *sessionWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.written = true
	if err := w.session.commit(w.ResponseWriter); err != nil {
		w.failed = true
		http.Error(w.ResponseWriter, "Internal server error", 500)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *sessionWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(200)
	}
	if w.failed {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
func (w *sessionWriter) Flush() {
	if !w.written {
		w.WriteHeader(200)
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}
func (s *Server) withBrowserSession(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request) {
	state := &browserSession{server: s, request: r}
	return &sessionWriter{ResponseWriter: w, session: state}, r.WithContext(context.WithValue(r.Context(), browserSessionKey{}, state))
}
func (s *Server) requestAuthentication(w http.ResponseWriter, r *http.Request) {
	browserState(r).set("return_to_after_authenticating", s.origin(r)+r.URL.RequestURI())
	http.Redirect(w, r, s.origin(r)+"/session/new", 302)
}
func (s *Server) postAuthenticationURL(r *http.Request) string {
	value := browserState(r).remove("return_to_after_authenticating")
	if location, ok := value.(string); ok && location != "" {
		return location
	}
	return s.origin(r) + "/"
}
func (s *Server) setAuthenticationCookie(w http.ResponseWriter, token string) error {
	expiry := s.DB.Now().UTC().AddDate(20, 0, 0)
	raw, err := s.Secrets.SignCookie("session_token", token, expiry)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(raw), Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode, Expires: expiry})
	return nil
}
func (s *Server) rememberRoom(w http.ResponseWriter, r *http.Request, id string) {
	if c, err := r.Cookie("last_room"); err == nil && c.Value == id {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "last_room", Value: id, Path: "/", Secure: s.Secure, SameSite: http.SameSiteLaxMode, Expires: s.DB.Now().AddDate(20, 0, 0)})
}
func (s *Server) consumeFlash(r *http.Request) (notice, alert string) {
	state := browserState(r)
	raw := state.remove("flash")
	flash, ok := raw.(map[string]any)
	if !ok {
		return
	}
	flashes, _ := flash["flashes"].(map[string]any)
	discarded, _ := flash["discard"].([]any)
	for _, key := range discarded {
		if name, ok := key.(string); ok {
			delete(flashes, name)
		}
	}
	notice, _ = flashes["notice"].(string)
	alert, _ = flashes["alert"].(string)
	return
}
func safeRedirect(location, origin string) bool {
	u, err := url.Parse(location)
	return err == nil && !strings.HasPrefix(location, "//") && (u.Host == "" || u.Scheme+"://"+u.Host == origin)
}

func (s *Server) flash(r *http.Request, name, message string) {
	browserState(r).set("flash", map[string]any{"discard": []any{}, "flashes": map[string]any{name: message}})
}
func (s *Server) requireUnauthenticated(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie("session_token")
	if err != nil {
		return true
	}
	var token string
	if s.Secrets.VerifyCookie("session_token", rails.UnescapeCookie(cookie.Value), s.DB.Now(), &token) != nil {
		return true
	}
	if _, err := s.DB.SessionUser(r.Context(), token); err != nil {
		return true
	}
	http.Redirect(w, r, s.origin(r)+"/", http.StatusFound)
	return false
}
