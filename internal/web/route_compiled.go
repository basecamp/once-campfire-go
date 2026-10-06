// Compiled route recognition. The legacy recognizer (legacyRecognize) scans
// the 177-entry contract table with one compiled regex per route on every
// request. This file compiles the same table once at startup into a
// segment-wise matcher: routes are grouped by method and first literal
// segment, and a request is matched with byte compares and scans instead of
// regexp execution. The contract semantics are preserved exactly — see
// route_contract.go and route_table.go — and any pattern class the compiler
// cannot prove identical to the regex form (or any request whose path needs
// regex-exclusive handling) falls back to the legacy recognizer. Never a
// wrong answer: the fallback unit is the whole table, because a request that
// would have matched an uncompiled route cannot be recognized safely any
// other way.
//
// CAMPFIRE_COMPILED_ROUTES (default on; off restores the regex recognizer for
// A/B and rollback) selects the matcher, parsed with the same value shapes as
// the other CAMPFIRE_* switches.
package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// parseCompiledRoutes maps a CAMPFIRE_COMPILED_ROUTES value to its setting,
// accepting the same shapes as parseRecordedPieces. An unrecognised value
// reports valid=false so the caller can warn while keeping the default on.
func parseCompiledRoutes(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

var (
	compiledRoutesOnce sync.Once
	compiledRoutesOn   atomic.Bool
)

// compiledRoutesEnabled reports whether the compiled recognizer is active.
// The flag is read once at first use; tests flip it through
// resetCompiledRoutesFlagForTest.
func compiledRoutesEnabled() bool {
	compiledRoutesOnce.Do(func() {
		on := true
		if raw, ok := os.LookupEnv("CAMPFIRE_COMPILED_ROUTES"); ok {
			var valid bool
			on, valid = parseCompiledRoutes(raw)
			if !valid {
				slog.Warn("invalid CAMPFIRE_COMPILED_ROUTES; keeping compiled routes on", "value", raw)
			}
		}
		compiledRoutesOn.Store(on)
		slog.Info("compiled route matching", "enabled", on)
	})
	return compiledRoutesOn.Load()
}

// resetCompiledRoutesFlagForTest forgets the cached flag so a test can flip
// CAMPFIRE_COMPILED_ROUTES and observe the new value on the next call.
func resetCompiledRoutesFlagForTest() {
	compiledRoutesOnce = sync.Once{}
}

// recognize dispatches to the compiled matcher when it is enabled and the
// table compiled fully, and to the regex recognizer otherwise; both paths
// must produce identical results (differential tests enforce this).
func recognize(method, path string) (*routeContract, map[string]string, error) {
	if compiledRoutesEnabled() {
		return compiledRecognize(method, path)
	}
	return legacyRecognize(method, path)
}

// --- compiled table ---------------------------------------------------------

const maxCRCaptures = 8 // capture names per route; a route with more rejects the table

type crStep struct {
	lit  string // literal bytes to match; a parameter when empty
	litB []byte // cached literal bytes
	name string // capture name ("" for a literal step)
	star bool   // star: consumes the rest of the path (final step only)
}

type crSeg struct{ steps []crStep }

type crRoute struct {
	contract   int      // index into contracts
	root       bool     // pattern is exactly "/"
	format     bool     // pattern ends in (.:format)
	first      string   // first literal segment: the group key
	names      []string // capture names in contract order
	segs       []crSeg
	bot        bool
	controller string
	action     string
}

var (
	compiledOnce  sync.Once
	compiledReady atomic.Bool
	compiledList  []crRoute
	compiledTable map[string]map[string][]int // method -> first segment -> contract indices
)

// buildCompiled parses every contract pattern into the compiled form. Any
// pattern the parser cannot prove identical to the legacy regex semantics
// disables the compiled table entirely: the recognizer then always takes the
// legacy path, so a request is never answered by a matcher that could be
// wrong about it.
func buildCompiled() {
	groups := make(map[string]map[string][]int)
	list := make([]crRoute, len(contracts))
	for i := range contracts {
		c := &contracts[i]
		cr, err := parseCR(c.Pattern)
		if err != nil {
			slog.Warn("compiled route matcher unavailable; using the regex recognizer", "pattern", c.Pattern, "error", err)
			return
		}
		cr.contract = i
		cr.bot = strings.Contains(c.Pattern, ":bot_key")
		endpoint := strings.SplitN(c.Endpoint, "#", 2)
		if len(endpoint) != 2 {
			slog.Warn("compiled route matcher unavailable; malformed endpoint", "pattern", c.Pattern, "endpoint", c.Endpoint)
			return
		}
		cr.controller, cr.action = endpoint[0], endpoint[1]
		list[i] = *cr
		g := groups[c.Method]
		if g == nil {
			g = make(map[string][]int)
			groups[c.Method] = g
		}
		g[cr.first] = append(g[cr.first], i)
	}
	compiledList = list
	compiledTable = groups
	compiledReady.Store(true)
}

var errUncompilable = errors.New("pattern shape not supported by the compiled matcher")

// parseCR compiles one contract pattern. It accepts the pattern shape the
// contract table actually uses — literal segments, :param segments, a final
// *star segment, and the optional trailing (.:format) — with the greedy
// semantics of the compiled regex (compileContract in route_contract.go):
// a param matches [^/.?]+, the star matches .+? with the trailing format
// group preferring to consume the final dot component, and the format group
// is exactly (?:\.([^/.?]+))?. Anything else is rejected so the table falls
// back to the legacy recognizer.
func parseCR(pattern string) (*crRoute, error) {
	cr := &crRoute{}
	if pattern == "/" {
		cr.root = true
		return cr, nil
	}
	if !strings.HasPrefix(pattern, "/") {
		return nil, errUncompilable
	}
	body := strings.TrimPrefix(pattern, "/")
	if strings.HasSuffix(body, "(.:format)") {
		cr.format = true
		body = strings.TrimSuffix(body, "(.:format)")
	}
	// The compiled regex treats '(' and ')' as glue for the optional format
	// group; any other group would change capture/property semantics.
	if strings.ContainsAny(body, "()") {
		return nil, errUncompilable
	}
	parts := strings.Split(body, "/")
	for pi, part := range parts {
		if part == "" {
			return nil, errUncompilable
		}
		var seg crSeg
		for i := 0; i < len(part); {
			if part[i] == ':' || part[i] == '*' {
				j := i + 1
				for j < len(part) && isIdentByte(part[j]) {
					j++
				}
				if j == i+1 {
					return nil, errUncompilable // empty name
				}
				step := crStep{name: part[i+1 : j]}
				if part[i] == '*' {
					if pi != len(parts)-1 || j != len(part) {
						return nil, errUncompilable // star must be the final step of the final segment
					}
					step.star = true
				} else if j != len(part) {
					// A param followed by literal text in the same segment
					// needs regex backtracking ([^/.?]+ is greedy and would
					// swallow the literal); the table has none, so reject
					// rather than risk a mismatch.
					return nil, errUncompilable
				}
				seg.steps = append(seg.steps, step)
				cr.names = append(cr.names, step.name)
				i = j
			} else {
				j := i
				for j < len(part) && part[j] != ':' && part[j] != '*' {
					j++
				}
				seg.steps = append(seg.steps, crStep{lit: part[i:j], litB: []byte(part[i:j])})
				i = j
			}
		}
		if len(seg.steps) == 0 {
			return nil, errUncompilable
		}
		cr.segs = append(cr.segs, seg)
	}
	if cr.format {
		cr.names = append(cr.names, "format")
	}
	if len(cr.segs) == 0 || len(cr.segs[0].steps) == 0 || cr.segs[0].steps[0].lit == "" {
		return nil, errUncompilable // grouping needs a literal first segment
	}
	if len(cr.names) > maxCRCaptures {
		return nil, errUncompilable
	}
	cr.first = cr.segs[0].steps[0].lit
	// A dot in the first segment would make the path-side group lookup
	// ambiguous (a stripped-suffix retry could reorder routes across
	// groups, and dot components can span the trailing format group); the
	// table has none, so reject rather than risk a mismatch.
	if strings.ContainsAny(cr.first, ".?") {
		return nil, errUncompilable
	}
	return cr, nil
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// upperHex uppercases a hex digit (a-f -> A-F; digits are unchanged).
func upperHex(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - ('a' - 'A')
	}
	return c
}

// normPath normalizes an escaped request path the way the contract regexes
// see it: runs of '/' collapse, and %xx escapes are upper-cased (see
// normalizedPath in route_contract.go). The returned buffer has no leading or
// trailing slash; segments are separated by single '/'.
func normPath(p string) []byte {
	b := make([]byte, 0, len(p))
	inSeg := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '/' {
			inSeg = false
			continue
		}
		if !inSeg {
			if len(b) > 0 {
				b = append(b, '/')
			}
			inSeg = true
		}
		if c == '%' && i+2 < len(p) && isHexByte(p[i+1]) && isHexByte(p[i+2]) {
			b = append(b, '%', upperHex(p[i+1]), upperHex(p[i+2]))
			i += 2
			continue
		}
		b = append(b, c)
	}
	return b
}

// pure reports whether s is a non-empty run of bytes matching [^/.?]+ — the
// body of a param or format capture.
func pure(s []byte) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c == '/' || c == '.' || c == '?' {
			return false
		}
	}
	return true
}

// match runs the route against the normalized path, filling caps with the raw
// capture substrings in names order. It returns the number of captures filled
// and whether the route matched the whole path. The format capture is only
// filled when the format group participates, so ok but n < len(names) means
// the format capture is empty — the same "" capture the regex reports.
func (cr *crRoute) match(b []byte, caps *[maxCRCaptures]string) (n int, ok bool) {
	if cr.root {
		return 0, len(b) == 0
	}
	pos := 0
	for si, seg := range cr.segs {
		if si > 0 {
			if pos >= len(b) || b[pos] != '/' {
				return 0, false
			}
			pos++
		}
		for _, st := range seg.steps {
			switch {
			case st.star:
				rem := b[pos:]
				if len(rem) == 0 {
					return 0, false
				}
				// (.+?)(?:\.([^/.?]+))?$: the lazy star prefers the trailing
				// format group, so the format is the final dot component when
				// its body is pure and the star still has at least one byte;
				// otherwise the star takes the whole remainder.
				p := -1
				for i := len(rem) - 1; i >= 0; i-- {
					if rem[i] == '.' {
						p = i
						break
					}
				}
				if p > 0 && pure(rem[p+1:]) {
					caps[n] = string(rem[:p])
					n++
					caps[n] = string(rem[p+1:])
					n++
				} else {
					caps[n] = string(rem)
					n++
				}
				pos = len(b)
			case st.lit != "":
				if !bytes.HasPrefix(b[pos:], st.litB) {
					return 0, false
				}
				pos += len(st.lit)
			default: // param: [^/.?]+
				j := pos
				for j < len(b) && b[j] != '/' && b[j] != '.' && b[j] != '?' {
					j++
				}
				if j == pos {
					return 0, false // params require at least one byte
				}
				caps[n] = string(b[pos:j])
				n++
				pos = j
			}
		}
	}
	// The optional (.:format) group follows the final segment for non-star
	// routes; star routes consumed it above.
	if cr.format {
		if pos < len(b) && b[pos] == '.' {
			rest := b[pos+1:]
			if !pure(rest) {
				return 0, false // e.g. ".x.y": the format cannot hold the second component
			}
			caps[n] = string(rest)
			n++
			pos = len(b)
		}
	}
	return n, pos == len(b)
}

// compiledRecognize is the compiled counterpart of legacyRecognize. It must
// produce identical results — same contract, same params map, same error —
// for every input; the differential tests enforce that, and anything the
// matcher cannot handle (a non-\n-free path, or a table that did not compile)
// delegates to legacyRecognize.
func compiledRecognize(method, path string) (*routeContract, map[string]string, error) {
	compiledOnce.Do(buildCompiled)
	if !compiledReady.Load() || strings.ContainsRune(path, '\n') {
		// '.' and [^...] never match "\n" in the regex forms while the
		// compiled scans would — fall back rather than risk a divergence.
		return legacyRecognize(method, path)
	}
	if method == "HEAD" {
		method = "GET"
	}
	b := normPath(path)
	group := compiledTable[method]
	if group == nil {
		return nil, nil, nil
	}
	first := string(b)
	if i := bytes.IndexByte(b, '/'); i >= 0 {
		first = string(b[:i])
	}
	routes := group[first]
	if routes == nil {
		// The whole path may be one segment whose format suffix belongs to
		// the route's trailing (.:format) group (e.g. "/webmanifest.json"
		// finds the "/webmanifest" group). First segments are dot-free by the
		// compile guard, so stripping the final dot component is exact.
		if d := strings.LastIndexByte(first, '.'); d > 0 && pure([]byte(first[d+1:])) {
			routes = group[first[:d]]
		}
	}
	var caps [maxCRCaptures]string
	for _, i := range routes {
		cr := &compiledList[i]
		n, ok := cr.match(b, &caps)
		if !ok {
			continue
		}
		c := &contracts[i]
		params := make(map[string]string, n+2)
		if cr.bot {
			params["format"] = "json"
		}
		for k := 0; k < n; k++ {
			raw := caps[k]
			if raw == "" {
				continue // an empty capture is dropped, never an empty value
			}
			value, err := url.PathUnescape(raw)
			if err != nil {
				return nil, nil, err
			}
			if !utf8.ValidString(value) {
				return nil, nil, errors.New("invalid UTF-8")
			}
			params[cr.names[k]] = value
		}
		params["controller"] = cr.controller
		params["action"] = cr.action
		return c, params, nil
	}
	return nil, nil, nil
}
