package richtext

import (
	"strconv"
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

func chomp(s string) string { return strings.TrimRight(s, "\r\n") }

// plain renders n's plain-text form into the arena and returns the view,
// byte-identical to the reference per-node string concatenation: text nodes
// are chomped of trailing CR/LF, block elements gain blank lines, list items
// gain bullets and indentation, blockquotes gain quotation marks.
func plain(a *xhtml.Arena, n *xhtml.Node) string {
	b := a.NewBuilder()
	appendPlain(&b, n)
	return b.String()
}

func appendPlain(b *xhtml.Builder, n *xhtml.Node) {
	if n.Type == xhtml.TextNode {
		b.WriteString(n.Data[:len(n.Data)-chompLen(n.Data)])
		return
	}
	if n.Type == xhtml.CommentNode {
		return
	}
	if n.Data == "script" || n.Data == "style" || n.Data == "unsupported" {
		return
	}
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
		text := subPlain(b, n)
		b.WriteString(text[:len(text)-chompLen(text)])
		b.WriteString("\n\n")
		return
	case "ul", "ol":
		if depth > 0 {
			b.PutByte('\n')
		}
		text := subPlain(b, n)
		b.WriteString(text[:len(text)-chompLen(text)])
		b.WriteString("\n\n")
		return
	case "br":
		b.PutByte('\n')
		return
	case "div":
		text := subPlain(b, n)
		b.WriteString(text[:len(text)-chompLen(text)])
		b.PutByte('\n')
		return
	case "figcaption":
		text := subPlain(b, n)
		b.PutByte('[')
		b.WriteString(text[:len(text)-chompLen(text)])
		b.PutByte(']')
		return
	case "blockquote":
		// Reference: text = chomp(children) + "\n\n"; the whole is trimmed,
		// quoted, and reassembled. Trimming chomped alone gives the same
		// trimmed text and offset — the appended "\n\n" is all whitespace.
		text := chomp(subPlain(b, n))
		trimmed := strings.Trim(text, " \t\n\v\f\r")
		if trimmed == "" {
			b.WriteString("“”")
			return
		}
		first := strings.Index(text, trimmed)
		b.WriteString(text[:first])
		b.WriteString("“")
		b.WriteString(trimmed)
		b.WriteString("”")
		b.WriteString(text[first+len(trimmed):])
		b.WriteString("\n\n")
		return
	case "li":
		bullet := "•"
		if list == "ol" {
			index := 1
			for prev := n.PrevSibling; prev != nil; prev = prev.PrevSibling {
				if prev.Type == xhtml.ElementNode {
					index++
				}
			}
			bullet = strconv.Itoa(index) + "."
		}
		indent := ""
		if depth > 1 {
			ib := b.A.NewBuilder()
			for i := 1; i < depth; i++ {
				ib.WriteString("  ")
			}
			indent = ib.String()
		}
		b.WriteString(indent)
		b.WriteString(bullet)
		b.WriteString(" ")
		text := subPlain(b, n)
		b.WriteString(text[:len(text)-chompLen(text)])
		b.PutByte('\n')
		return
	default:
		appendChildrenPlain(b, n)
	}
}

// subPlain renders the children of n into a fresh arena region and returns
// the view — the reference builds the same text into a per-node builder and
// transforms it per case, so the cases above slice the same way.
func subPlain(b *xhtml.Builder, n *xhtml.Node) string {
	sub := b.A.NewBuilder()
	appendChildrenPlain(&sub, n)
	return sub.String()
}

func appendChildrenPlain(b *xhtml.Builder, n *xhtml.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		appendPlain(b, c)
	}
}

// chompLen returns the number of trailing \r and \n bytes in s.
func chompLen(s string) int {
	n := len(s)
	for n > 0 && (s[n-1] == '\r' || s[n-1] == '\n') {
		n--
	}
	return len(s) - n
}
