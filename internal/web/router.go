package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Rails resolves overlapping routes in declaration order. ServeMux rejects some
// of Campfire's valid overlaps (rooms/opens/:id/edit and rooms/:id/messages/:id).
// This small adapter retains that ordering while using standard HTTP handlers.
type router struct{ routes []route }
type route struct {
	method  string
	pattern *regexp.Regexp
	names   []string
	handler http.HandlerFunc
}

func (m *router) HandleFunc(pattern string, handler http.HandlerFunc) {
	method, path, _ := strings.Cut(pattern, " ")
	parts := strings.Split(path, "/")
	names := []string{}
	for i, part := range parts {
		if part == "{$}" {
			parts[i] = ""
		} else if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			if strings.HasSuffix(name, "...") {
				names = append(names, strings.TrimSuffix(name, "..."))
				parts[i] = "(.+)"
			} else {
				names = append(names, name)
				parts[i] = "([^/]+)"
			}
		} else {
			parts[i] = regexp.QuoteMeta(part)
		}
	}
	m.routes = append(m.routes, route{method, regexp.MustCompile("^" + strings.Join(parts, "/") + "$"), names, handler})
}
func (m *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, route := range m.routes {
		if route.method != r.Method && !(r.Method == "HEAD" && route.method == "GET") {
			continue
		}
		values := route.pattern.FindStringSubmatch(r.URL.EscapedPath())
		if values == nil {
			continue
		}
		for i, name := range route.names {
			value, err := url.PathUnescape(values[i+1])
			if err != nil {
				http.Error(w, "Invalid path", 400)
				return
			}
			r.SetPathValue(name, value)
		}
		route.handler(w, r)
		return
	}
	http.NotFound(w, r)
}
