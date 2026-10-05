package views

import (
	_ "embed"
	"strings"
)

// Views for reference/app/views/pwa (the reference's pwa.rs).

// ServiceWorkerJS is pwa/service_worker.js, served verbatim (a copy of the reference's
// templates/pwa/service_worker.js).
//
//go:embed pwa_service_worker.js
var ServiceWorkerJS string

// ManifestImageURL is pwa/manifest.json's image_url(source).
func ManifestImageURL(baseURL string, assetPath func(string) string, source string) string {
	return baseURL + assetPath(source)
}

// ManifestJSON is value as a JSON string, quotes included, as serde_json writes it. ERB
// HTML-escaped the manifest's values into the JSON, so an account named `a\b` or `"a"` made it
// invalid and the logo URL came out as `?size=small&amp;v=...`; the values are JSON strings here.
func ManifestJSON(value string) HTML {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	last := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		var escape string
		switch {
		case c == '"':
			escape = `\"`
		case c == '\\':
			escape = `\\`
		case c == '\b':
			escape = `\b`
		case c == '\f':
			escape = `\f`
		case c == '\n':
			escape = `\n`
		case c == '\r':
			escape = `\r`
		case c == '\t':
			escape = `\t`
		case c < 0x20:
			escape = string([]byte{'\\', 'u', '0', '0', hex[c>>4], hex[c&0xf]})
		default:
			continue
		}
		b.WriteString(value[last:i])
		b.WriteString(escape)
		last = i + 1
	}
	b.WriteString(value[last:])
	b.WriteByte('"')
	return HTML(b.String())
}
