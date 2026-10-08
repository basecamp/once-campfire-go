package engine

import "net/http"

// routeTable is the compiled ownership map: method -> escaped path -> handler.
//
// Keys are the exact r.URL.EscapedPath() strings the legacy router recognizes
// (see Engine.ServeHTTP for the normalization caveat). The shape is
// deliberately small. ENGINE-11 ships it empty (nothing is owned, so the
// engine is a pure pass-through) and ENGINE-16 fills it with the real routes.
// Lookups are exact method and escaped-path matches; dynamic patterns get their
// own matching, including normalizedPath semantics, when the first one lands.
type routeTable struct {
	byMethod map[string]map[string]http.Handler
}

func (t *routeTable) add(method, path string, handler http.Handler) {
	paths := t.byMethod[method]
	if paths == nil {
		paths = make(map[string]http.Handler)
		if t.byMethod == nil {
			t.byMethod = make(map[string]map[string]http.Handler, 4)
		}
		t.byMethod[method] = paths
	}
	paths[path] = handler
}

func (t *routeTable) lookup(method, path string) (http.Handler, bool) {
	if t.byMethod == nil {
		return nil, false
	}
	paths := t.byMethod[method]
	if paths == nil {
		return nil, false
	}
	handler, ok := paths[path]
	return handler, ok
}

// registerRoutes installs the compiled owned routes. ENGINE-11 owns nothing:
// every request falls back to legacy. Dynamic routes (rooms, messages,
// sidebar) land here starting with ENGINE-16, one differential-validated route
// at a time.
func registerRoutes(e *Engine) {
}
