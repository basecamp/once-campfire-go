package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Rails resolves overlapping routes in declaration order. ServeMux rejects some
// of Campfire's valid overlaps (rooms/opens/:id/edit and rooms/:id/messages/:id).
// This small adapter retains that ordering while using standard HTTP handlers.
type router struct {
	routes []route
	once   sync.Once
	cmux   *compiledMux // compiled form of routes, built on first use
}
type route struct {
	method  string
	pattern string // the registration pattern, kept for the compiled matcher
	regex   *regexp.Regexp
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
	m.routes = append(m.routes, route{method, pattern, regexp.MustCompile("^" + strings.Join(parts, "/") + "$"), names, handler})
}

// ServeHTTP matches against the compiled route table when
// CAMPFIRE_COMPILED_ROUTES is enabled and every registered route compiled
// (buildCompiledMux returns nil otherwise), and against the per-route regexes
// when it is off. Both paths must produce identical results; the differential
// tests in route_compiled_test.go enforce that.
func (m *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if compiledRoutesEnabled() {
		if m.serveCompiled(w, r) {
			return
		}
	}
	m.serveLegacy(w, r)
}

// serveLegacy is the per-route regex scan, unchanged by the compiled matcher.
func (m *router) serveLegacy(w http.ResponseWriter, r *http.Request) {
	for _, route := range m.routes {
		if route.method != r.Method && !(r.Method == "HEAD" && route.method == "GET") {
			continue
		}
		values := route.regex.FindStringSubmatch(r.URL.EscapedPath())
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

// --- compiled mux -----------------------------------------------------------

// muxSeg is one path segment of a compiled mux route: a literal (lit, which
// may be empty for "{$}" and the leading "" of every pattern), a param, or a
// trailing star.
type muxSeg struct {
	lit   string
	param string // param name; the segment is a literal when empty
	star  bool
}

type muxRoute struct {
	handler http.HandlerFunc
	segs    []muxSeg
	names   []string // capture names in pattern order
}

// compiledMux is the compiled mux table: routes grouped by method and first
// segment so a request scans only the routes that could possibly match it,
// in registration order.
type compiledMux struct {
	routes []muxRoute
	groups map[string]map[string][]int // method -> first segment -> route index
}

// buildCompiledMux compiles the registered routes into segment-wise matches.
// The mux pattern language is literal segments, {name} params ([^/]+), a
// final {name...} star ((.+)), and "{$}" for a literal empty segment. Any
// shape outside that (a non-final star, for example, whose regex semantics
// need backtracking) returns nil, which keeps the regex recognizer for the
// whole mux — never a wrong compiled answer.
func buildCompiledMux(routes []route) *compiledMux {
	cm := &compiledMux{groups: make(map[string]map[string][]int)}
	for i, rt := range routes {
		method, pattern, _ := strings.Cut(rt.pattern, " ")
		parts := strings.Split(pattern, "/")
		if len(parts) < 1 {
			return nil
		}
		var mr muxRoute
		mr.handler = rt.handler
		ok := true
		for pi, part := range parts {
			var seg muxSeg
			switch {
			case part == "{$}":
				seg.lit = ""
			case strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}"):
				name := part[1 : len(part)-1]
				if strings.HasSuffix(name, "...") {
					if pi != len(parts)-1 {
						ok = false
						break
					}
					seg.star = true
					name = strings.TrimSuffix(name, "...")
				}
				seg.param = name
				mr.names = append(mr.names, name)
			default:
				seg.lit = part
			}
			mr.segs = append(mr.segs, seg)
		}
		if !ok || len(mr.names) > maxCRCaptures {
			return nil
		}
		// The first non-empty path segment of every registered pattern is a
		// literal; the group key is parts[1]. A param there would make the
		// group lookup ambiguous.
		if len(parts) < 2 || (parts[1] != "{$}" && strings.HasPrefix(parts[1], "{") && strings.HasSuffix(parts[1], "}")) {
			return nil
		}
		first := parts[1]
		if first == "{$}" {
			first = ""
		}
		cm.routes = append(cm.routes, mr)
		g := cm.groups[method]
		if g == nil {
			g = make(map[string][]int)
			cm.groups[method] = g
		}
		g[first] = append(g[first], i)
	}
	return cm
}

func (mr *muxRoute) match(segs []string, caps *[maxCRCaptures]string) (n int, ok bool) {
	pos := 0
	for _, seg := range mr.segs {
		if seg.star {
			if pos >= len(segs) {
				return n, false // (.+) needs at least one byte
			}
			v := strings.Join(segs[pos:], "/")
			if v == "" {
				return n, false // (.+) needs at least one byte
			}
			caps[n] = v
			n++
			return n, true // the star is the final segment
		}
		if pos >= len(segs) {
			return n, false
		}
		if seg.param != "" {
			if segs[pos] == "" {
				return n, false // [^/]+ needs at least one byte
			}
			caps[n] = segs[pos]
			n++
		} else if segs[pos] != seg.lit {
			return n, false
		}
		pos++
	}
	return n, pos == len(segs)
}

// mergedCandidates returns the candidate route indices for a request method,
// in registration order. HEAD requests match HEAD-registered routes and GET
// routes, merged by their registration order.
func (cm *compiledMux) mergedCandidates(method, first string) []int {
	if method != "HEAD" {
		if g := cm.groups[method]; g != nil {
			return g[first]
		}
		return nil
	}
	hg, gg := cm.groups["HEAD"], cm.groups["GET"]
	var a, b []int
	if hg != nil {
		a = hg[first]
	}
	if gg != nil {
		b = gg[first]
	}
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make([]int, 0, len(a)+len(b))
	for i, j := 0, 0; i < len(a) || j < len(b); {
		if j >= len(b) || i < len(a) && a[i] < b[j] {
			merged = append(merged, a[i])
			i++
		} else if i >= len(a) || b[j] < a[i] {
			merged = append(merged, b[j])
			j++
		} else {
			merged = append(merged, a[i])
			i++
			j++
		}
	}
	return merged
}

// serveCompiled handles the request through the compiled table. It returns
// false when the compiled table is unavailable (uncompilable route, or a
// path the compiled scans would not match identically), so the caller can
// fall back to the regex recognizer.
func (m *router) serveCompiled(w http.ResponseWriter, r *http.Request) bool {
	m.once.Do(func() { m.cmux = buildCompiledMux(m.routes) })
	cm := m.cmux
	if cm == nil {
		return false
	}
	esc := r.URL.EscapedPath()
	if strings.ContainsRune(esc, '\n') {
		// "." in the star regex never matches "\n"; the compiled star would.
		return false
	}
	segs := strings.Split(esc, "/")
	first := ""
	if len(segs) > 1 {
		first = segs[1]
	}
	cand := cm.mergedCandidates(r.Method, first)
	var caps [maxCRCaptures]string
	for _, i := range cand {
		mr := &cm.routes[i]
		n, ok := mr.match(segs, &caps)
		if !ok {
			continue
		}
		for k := 0; k < n; k++ {
			value, err := url.PathUnescape(caps[k])
			if err != nil {
				http.Error(w, "Invalid path", 400)
				return true
			}
			r.SetPathValue(mr.names[k], value)
		}
		mr.handler(w, r)
		return true
	}
	http.NotFound(w, r)
	return true
}
