package web

import (
	"errors"
	"github.com/basecamp/once-campfire-go/internal/database"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

type routeContract struct {
	Method, Pattern, Endpoint, Action string
	regex                             *regexp.Regexp
	names                             []string
	bot                               bool
}

func compileContract(pattern string) (*regexp.Regexp, []string) {
	var b strings.Builder
	b.WriteByte('^')
	var names []string
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '(':
			b.WriteString("(?:")
		case ')':
			b.WriteString(")?")
		case ':', '*':
			kind := pattern[i]
			j := i + 1
			for j < len(pattern) && (pattern[j] >= 'a' && pattern[j] <= 'z' || pattern[j] >= 'A' && pattern[j] <= 'Z' || pattern[j] >= '0' && pattern[j] <= '9' || pattern[j] == '_') {
				j++
			}
			names = append(names, pattern[i+1:j])
			if kind == ':' {
				b.WriteString("([^/.?]+)")
			} else {
				b.WriteString("(.+?)")
			}
			i = j - 1
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteByte('$')
	return regexp.MustCompile(b.String()), names
}

var escapedHex = regexp.MustCompile(`%[a-fA-F0-9]{2}`)

func normalizedPath(path string) string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	return escapedHex.ReplaceAllStringFunc("/"+strings.Join(parts, "/"), strings.ToUpper)
}
func recognize(method, path string) (*routeContract, map[string]string, error) {
	path = normalizedPath(path)
	if method == "HEAD" {
		method = "GET"
	}
	for i := range contracts {
		route := &contracts[i]
		if route.Method != method {
			continue
		}
		captures := route.regex.FindStringSubmatch(path)
		if captures == nil {
			continue
		}
		params := map[string]string{}
		if route.bot {
			params["format"] = "json"
		}
		for j, name := range route.names {
			if captures[j+1] == "" {
				continue
			}
			value, err := url.PathUnescape(captures[j+1])
			if err != nil {
				return nil, nil, err
			}
			if !utf8.ValidString(value) {
				return nil, nil, errors.New("invalid UTF-8")
			}
			params[name] = value
		}
		parts := strings.SplitN(route.Endpoint, "#", 2)
		params["controller"] = parts[0]
		params["action"] = parts[1]
		return route, params, nil
	}
	return nil, nil, nil
}
func init() {
	for i := range contracts {
		contracts[i].regex, contracts[i].names = compileContract(contracts[i].Pattern)
		contracts[i].bot = strings.Contains(contracts[i].Pattern, ":bot_key")
	}
}
func (s *Server) routeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/cable" {
		s.mux.ServeHTTP(w, r)
		return
	}
	route, params, err := recognize(r.Method, r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "Invalid path parameters", 400)
		return
	}
	if route == nil || route.Action == "action_not_found" {
		publicError(w, r, 404)
		return
	}
	if route.Action == "missing_controller" {
		publicError(w, r, 500)
		return
	}
	for key, value := range params {
		r.SetPathValue(key, value)
	}
	path := normalizedPath(r.URL.EscapedPath())
	if format := params["format"]; format != "" {
		suffix := "." + url.PathEscape(format)
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
		}
	}
	clone := *r.URL
	clone.Path, _ = url.PathUnescape(path)
	clone.RawPath = path
	r.URL = &clone
	if strings.HasPrefix(route.Action, "mailbox::") {
		if route.Action == "mailbox::conductor" {
			w.WriteHeader(403)
		} else {
			w.WriteHeader(404)
		}
		return
	}
	if strings.HasPrefix(route.Action, "turbo_native::") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		text := map[string]string{"turbo_native::recede": "Going back…", "turbo_native::resume": "Staying put…", "turbo_native::refresh": "Refreshing…"}[route.Action]
		w.Write([]byte(text))
		return
	}
	if route.Action == "rooms::destroy_without_room" || route.Action == "rooms::directs::show" {
		s.auth(func(w http.ResponseWriter, r *http.Request, _ database.User) {
			publicError(w, r, 500)
		})(w, r)
		return
	}
	if route.Action == "rooms::index" {
		s.auth(s.roomsIndexView)(w, r)
		return
	}
	if route.bot {
		if !s.botRequestViews(w, r, route.Action) {
			publicError(w, r, 404)
		}
		return
	}
	if strings.HasPrefix(route.Endpoint, "messages#") && params["room_id"] == "" {
		r.SetPathValue("id", "")
	}
	s.mux.ServeHTTP(w, r)
}
