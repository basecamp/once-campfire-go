package web

import (
	"errors"
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
	index                             int
	controller, endpointAction        string
}

// Cable is a protocol endpoint, not part of the Rails controller vector. Its
// exact path is deliberately not normalized into a new upgrade target.
var cableRoute = routeContract{Method: "GET", Pattern: "/cable", Endpoint: "cable#show", Action: "cable::show"}

type routeGroup struct{ method, segment string }

var routeGroups = make(map[routeGroup][]*routeContract)

func firstSegment(path string) string {
	segment, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return segment
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
	if strings.HasPrefix(path, "/") && (len(path) == 1 || !strings.HasSuffix(path, "/")) &&
		!strings.Contains(path, "//") && !strings.Contains(path, "%") {
		return path
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	return escapedHex.ReplaceAllStringFunc("/"+strings.Join(parts, "/"), strings.ToUpper)
}

func recognize(method, path string) (*routeContract, map[string]string, error) {
	if method == "HEAD" {
		method = "GET"
	}
	path = normalizedPath(path)
	// Every controller route has a literal first segment. This index excludes
	// impossible matches without changing declaration order within a group.
	segment, _, _ := strings.Cut(firstSegment(path), ".")
	for _, route := range routeGroups[routeGroup{method, segment}] {
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
		params["controller"] = route.controller
		params["action"] = route.endpointAction
		return route, params, nil
	}
	return nil, nil, nil
}

// Recognition is pure in method/path. Method overrides select a new match;
// dispatch leaves the original URL intact and reuses its selected captures.
type recognizedRoute struct {
	method, path string
	route        *routeContract
	params       map[string]string
	err          error
}

func recognizeRequest(r *http.Request) (*routeContract, map[string]string, error) {
	info := requestMetadata(r.Context())
	path := r.URL.EscapedPath()
	if r.URL.Path == cableRoute.Pattern {
		return &cableRoute, nil, nil
	}
	if info == nil {
		return recognize(r.Method, path)
	}
	if match := info.routing; match != nil && match.method == r.Method && match.path == path {
		return match.route, match.params, match.err
	}
	route, params, err := recognize(r.Method, path)
	info.routing = &recognizedRoute{
		method: r.Method,
		path:   path,
		route:  route,
		params: params,
		err:    err,
	}
	return route, params, err
}

func init() {
	for i := range contracts {
		route := &contracts[i]
		route.regex, route.names = compileContract(route.Pattern)
		route.bot = strings.Contains(route.Pattern, ":bot_key")
		route.index = i
		route.controller, route.endpointAction, _ = strings.Cut(route.Endpoint, "#")
		segment, _, _ := strings.Cut(firstSegment(route.Pattern), "(")
		group := routeGroup{route.Method, segment}
		routeGroups[group] = append(routeGroups[group], route)
	}
	cableRoute.index = len(contracts)
}

func (s *Server) routeHTTP(w http.ResponseWriter, r *http.Request) {
	route, params, err := recognizeRequest(r)
	if err != nil {
		http.Error(w, "Invalid path parameters", 400)
		return
	}
	if route == nil {
		publicError(w, r, 404)
		return
	}
	for key, value := range params {
		r.SetPathValue(key, value)
	}
	s.dispatch[route.index](w, r)
}
