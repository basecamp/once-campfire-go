package engine

import "net/http"

// routeTable is the compiled ownership map: method -> escaped path -> handler.
//
// Keys are the exact r.URL.EscapedPath() strings the legacy router recognizes
// (see Engine.ServeHTTP for the normalization caveat). The shape is
// deliberately small. Production leaves it empty, so this scaffold is a pure
// pass-through; the optimized production router and handlers live in web.
// Lookups are exact method and escaped-path matches; dynamic patterns get their
// own matching, including normalizedPath semantics, if ownership is promoted.
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

// registerRoutes intentionally installs no production routes. The compiled
// recognizer and fast handlers run inside web.Server under their own switches
// (including CAMPFIRE_COMPILED_ROUTES), not CAMPFIRE_ENGINE. Keep this scaffold
// as a pass-through until a separately validated ownership migration is made.
func registerRoutes(e *Engine) {
}
