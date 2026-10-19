// Package richtext implements the reference Action Text and Lexxy pipeline.
package richtext

import (
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

// Render is the context-free entry point for messages without user attachments.
func Render(body string) (string, string) {
	result, _ := Process(body, Context{})
	return result.Presentation, result.Plain
}

// Canonical mirrors assignment to an Action Text body. Parse failures retain the
// original input, as the Rust controller does; presentation still sanitizes it.
func Canonical(body string) string {
	a := acquireArena()
	defer releaseArena(a)
	root, err := load(a, body)
	if err != nil {
		return body
	}
	return strings.Clone(serialize(a, root))
}

// StripTags matches Rails' FullSanitizer followed by the default sanitizer.
// Text nodes are concatenated without the block separators of to_plain_text.
func StripTags(body string) (string, error) {
	a := acquireArena()
	defer releaseArena(a)
	root, err := parse(a, body)
	if err != nil {
		return "", err
	}
	text := a.NewBuilder()
	var visit func(*xhtml.Node)
	visit = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			text.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	sanitized, err := sanitizeString(a, escapeText(text.String()))
	if err != nil {
		return "", err
	}
	return strings.Clone(sanitized), nil
}
