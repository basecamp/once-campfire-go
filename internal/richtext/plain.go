package richtext

import (
	"fmt"
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

func chomp(s string) string { return strings.TrimRight(s, "\r\n") }
func plain(n *xhtml.Node) string {
	depth, list := 0, ""
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Data == "ul" || p.Data == "ol" {
			depth++
			if list == "" {
				list = p.Data
			}
		}
	}
	index := 1
	if n.Data == "li" && list == "ol" {
		for prev := n.PrevSibling; prev != nil; prev = prev.PrevSibling {
			if prev.Type == xhtml.ElementNode {
				index++
			}
		}
	}
	return plainAt(n, depth, list, index)
}

// Subtree callers inherit their existing list context once. Descendants carry
// that context and their element-sibling ordinal instead of rescanning the tree.
func plainAt(n *xhtml.Node, depth int, list string, index int) string {
	if n.Type == xhtml.TextNode {
		return chomp(n.Data)
	}
	if n.Type == xhtml.CommentNode {
		return ""
	}
	if n.Data == "script" || n.Data == "style" || n.Data == "unsupported" {
		return ""
	}
	childDepth, childList := depth, list
	if n.Data == "ul" || n.Data == "ol" {
		childDepth++
		childList = n.Data
	}
	var b strings.Builder
	childIndex := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode {
			childIndex++
		}
		b.WriteString(plainAt(c, childDepth, childList, childIndex))
	}
	text := b.String()
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
