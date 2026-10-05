// Package engine is the strangler seam in front of the legacy application.
//
// The engine owns an explicit, compiled route table. A request whose method
// and path match an owned route is served by the engine; every other request
// is handed to the legacy handler untouched (the "fallback"), which keeps the
// byte-for-byte behavior of the port and gives the differential harness an
// oracle. With an empty table — ENGINE-11 ships exactly that — the engine is a
// pure pass-through.
package engine

import (
	"net/http"
	"sync/atomic"
)

// Mode selects how much of the request stream the engine owns.
type Mode uint8

const (
	// ModeOff sends every request to the legacy handler. It is the A/B and
	// rollback switch (CAMPFIRE_ENGINE=off).
	ModeOff Mode = iota
	// ModeOn is the production default: owned routes run in the engine and
	// everything else falls back to legacy.
	ModeOn
	// ModeForce serves owned routes exactly as ModeOn does. It exists for
	// tests and the differential harness, which pin "the engine must own this
	// route" without depending on which routes production has promoted.
	ModeForce
)

func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeForce:
		return "force"
	default:
		return "on"
	}
}

// ParseMode maps a CAMPFIRE_ENGINE value to a Mode. The empty string, "on",
// and unrecognized values select ModeOn, the production default; "off" and
// "force" are exact matches.
func ParseMode(value string) Mode {
	switch value {
	case "off":
		return ModeOff
	case "force":
		return ModeForce
	default:
		return ModeOn
	}
}

// Config carries the engine's production wiring options.
type Config struct {
	// Mode controls route ownership. The zero value is ModeOff; callers wire
	// ParseMode(os.Getenv("CAMPFIRE_ENGINE")) so production runs ModeOn.
	Mode Mode
}

// Engine is the root http.Handler placed at the front.Serve seam. It owns the
// compiled route table and delegates everything else to the legacy handler.
type Engine struct {
	legacy    http.Handler
	mode      Mode
	routes    routeTable
	fallbacks atomic.Int64
}

// New builds the engine around the legacy handler. The legacy handler is never
// inspected beyond being called for fallback requests; cmd/campfire passes the
// pre-composed front.Deflate(webServer) so fallback routes keep the encoding
// chain they had before the engine existed.
func New(legacy http.Handler, cfg Config) *Engine {
	e := &Engine{legacy: legacy, mode: cfg.Mode}
	registerRoutes(e)
	return e
}

// Fallbacks returns the number of requests delegated to the legacy handler.
// Owned routes never count; the benchmark gate is that the counter stays zero
// on the owned corpus. In ModeOff every request is a fallback.
func (e *Engine) Fallbacks() int64 { return e.fallbacks.Load() }

func (e *Engine) enabled() bool { return e.mode != ModeOff }

// ServeHTTP runs an owned route or delegates to legacy. The decision is one
// map lookup; on a miss the request reaches the legacy handler untouched apart
// from the counter bump.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if e.enabled() {
		if handler, ok := e.routes.lookup(r.Method, r.URL.Path); ok {
			e.serve(handler, w, r)
			return
		}
	}
	e.fallbacks.Add(1)
	e.legacy.ServeHTTP(w, r)
}

// serve runs one owned route. ENGINE-16 replaces the body with the fast
// pipeline; for now an owned route is just its compiled handler, which owns
// its own encoding.
func (e *Engine) serve(handler http.Handler, w http.ResponseWriter, r *http.Request) {
	handler.ServeHTTP(w, r)
}

// handle registers a compiled route. Production routes are installed by
// registerRoutes in router.go; tests use it in-package to install probe
// routes. It is unexported because route ownership is compiled into the binary.
func (e *Engine) handle(method, path string, handler http.Handler) {
	e.routes.add(method, path, handler)
}
