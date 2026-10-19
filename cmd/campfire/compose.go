package main

import (
	"net/http"

	"github.com/basecamp/once-campfire-go/internal/engine"
	"github.com/basecamp/once-campfire-go/internal/front"
)

// engineRoot builds the engine. It is a variable so the composition test can
// inject a probe root without an exported route-registration seam in the
// engine package (route ownership is compiled in there).
var engineRoot = func(legacy http.Handler, mode engine.Mode) http.Handler {
	return engine.New(legacy, engine.Config{Mode: mode})
}

// buildRoot composes the handler passed to front.Serve. The engine wraps the
// legacy handler already wrapped in front.Deflate, so fallback responses keep
// the pre-engine bodyLimit(Deflate(app)) chain byte for byte, while routes the
// engine serves bypass Deflate and encode themselves.
func buildRoot(app http.Handler, mode engine.Mode) http.Handler {
	return engineRoot(front.Deflate(app), mode)
}

// rootConfig returns the front configuration matching buildRoot: the
// application owns response encoding, so front.Serve must not add its own
// Deflate wrap. The body limit still applies inside Serve.
func rootConfig(config front.Config) front.Config {
	config.SkipDeflate = true
	return config
}
