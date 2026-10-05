// Package views renders Campfire's pages byte for byte as the reference's askama views
// (reference/crates/views) do. One quicktemplate file per reference template, converted with
// bin/askama2qtpl so its text and whitespace are the template's own; the view-model types and
// helpers mirror the reference crate's.
//
// Output is escaped exactly like ERB (`{%s %}`: `&amp; &lt; &gt; &quot; &#39;`, which is
// quicktemplate's own escaping); HTML values are html_safe and written as they are (`{%s= %}`).
package views

import "strings"

// HTML is an html_safe string (askama's Safe<String>, Rails' SafeBuffer): written unescaped.
type HTML string

// Escape is ERB::Util.html_escape.
func Escape(s string) string {
	if strings.IndexAny(s, `&<>"'`) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	EscapeTo(&b, s)
	return b.String()
}

// EscapeTo appends ERB::Util.html_escape(s) to b, a run of unescaped bytes at a time.
func EscapeTo(b *strings.Builder, s string) {
	last := 0
	for i := 0; i < len(s); i++ {
		var replacement string
		switch s[i] {
		case '&':
			replacement = "&amp;"
		case '<':
			replacement = "&lt;"
		case '>':
			replacement = "&gt;"
		case '"':
			replacement = "&quot;"
		case '\'':
			replacement = "&#39;"
		default:
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(replacement)
		last = i + 1
	}
	b.WriteString(s[last:])
}

// Text is h(text) as an html_safe value.
func Text(s string) HTML { return HTML(Escape(s)) }
