// Package richtext implements the reference Action Text and Lexxy pipeline.
package richtext

import (
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

// Canonical mirrors assignment to an Action Text body. Parse failures retain the
// original input, as the Rust controller does; presentation still sanitizes it.
func Canonical(body string) string {
	root, err := load(body)
	if err != nil {
		return body
	}
	return serialize(root)
}

// StripTags matches Rails' FullSanitizer followed by the default sanitizer.
// Text nodes are concatenated without the block separators of to_plain_text.
func StripTags(body string) (string, error) {
	root, err := parse(body)
	if err != nil {
		return "", err
	}
	var text strings.Builder
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
	return sanitizeString(escapeText(text.String()))
}
