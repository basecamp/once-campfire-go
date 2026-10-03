package richtext

import (
	"fmt"
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

func chomp(s string) string { return strings.TrimRight(s, "\r\n") }
func plain(n *xhtml.Node) string {
	if n.Type == xhtml.TextNode {
		return chomp(n.Data)
	}
	if n.Type == xhtml.CommentNode {
		return ""
	}
	if n.Data == "script" || n.Data == "style" || n.Data == "unsupported" {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(plain(c))
	}
	text := b.String()
	depth := 0
	list := ""
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Data == "ul" || p.Data == "ol" {
			depth++
			if list == "" {
				list = p.Data
			}
		}
	}
	switch n.Data {
	case "p", "h1":
		return chomp(text) + "\n\n"
	case "ul", "ol":
		if depth > 0 {
			return "\n" + chomp(text) + "\n\n"
		}
		return chomp(text) + "\n\n"
	case "br":
		return "\n"
	case "div":
		return chomp(text) + "\n"
	case "figcaption":
		return "[" + chomp(text) + "]"
	case "blockquote":
		text = chomp(text) + "\n\n"
		trimmed := strings.Trim(text, " \t\n\v\f\r")
		if trimmed == "" {
			return "“”"
		}
		first := strings.Index(text, trimmed)
		return text[:first] + "“" + trimmed + "”" + text[first+len(trimmed):]
	case "li":
		bullet := "•"
		if list == "ol" {
			index := 1
			for prev := n.PrevSibling; prev != nil; prev = prev.PrevSibling {
				if prev.Type == xhtml.ElementNode {
					index++
				}
			}
			bullet = fmt.Sprintf("%d.", index)
		}
		indent := ""
		if depth > 1 {
			indent = strings.Repeat("  ", depth-1)
		}
		return indent + bullet + " " + chomp(text) + "\n"
	}
	return text
}
