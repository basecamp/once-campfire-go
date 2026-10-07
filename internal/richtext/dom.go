package richtext

import (
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
	"golang.org/x/net/html/atom"
)

// parse runs the arena-backed fragment parse: every node and string of the
// returned tree lives in a, valid until a's next Reset. Nothing on the tree
// may outlive the call that owns a.
func parse(a *xhtml.Arena, body string) (*xhtml.Node, error) {
	return parseIn(a, body, nil)
}
func parseIn(a *xhtml.Arena, body string, context *xhtml.Node) (*xhtml.Node, error) {
	if context == nil || context.Type != xhtml.ElementNode {
		context = a.AllocNode(xhtml.ElementNode, atom.Body, "body", "", nil)
	} else {
		context = a.AllocNode(xhtml.ElementNode, context.DataAtom, context.Data, context.Namespace, nil)
	}
	nodes, err := a.ParseFragment(body, context, xhtml.ParseOptionEnableScripting(false))
	if err != nil {
		return nil, err
	}
	root := a.AllocNode(xhtml.DocumentNode, 0, "", "", nil)
	for _, node := range nodes {
		root.AppendChild(node)
	}
	return root, nil
}
func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func setAttr(a *xhtml.Arena, n *xhtml.Node, key, value string) {
	for i, at := range n.Attr {
		if at.Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = a.AttrAppend(n.Attr, xhtml.Attribute{Key: key, Val: value})
}

// walk visits n post-order. The child iteration captures each successor
// before recursion because fn may mutate the tree (replace/remove children of
// the node being visited) — the snapshot semantics of the previous
// children() slice, without the per-node allocation.
func walk(n *xhtml.Node, fn func(*xhtml.Node)) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		walk(c, fn)
		c = next
	}
	fn(n)
}
func clone(a *xhtml.Arena, n *xhtml.Node) *xhtml.Node {
	out := a.CloneNode(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out.AppendChild(clone(a, c))
	}
	return out
}
func replace(a *xhtml.Arena, n *xhtml.Node, markup string) error {
	root, err := parseIn(a, markup, n.Parent)
	if err != nil {
		return err
	}
	if n.Parent == nil {
		return nil
	}
	for {
		c := root.FirstChild
		if c == nil {
			break
		}
		root.RemoveChild(c)
		n.Parent.InsertBefore(c, n)
	}
	n.Parent.RemoveChild(n)
	return nil
}
func inner(a *xhtml.Arena, n *xhtml.Node, markup string) error {
	root, err := parseIn(a, markup, n)
	if err != nil {
		return err
	}
	for {
		if c := n.FirstChild; c != nil {
			n.RemoveChild(c)
		} else {
			break
		}
	}
	for {
		c := root.FirstChild
		if c == nil {
			break
		}
		root.RemoveChild(c)
		n.AppendChild(c)
	}
	return nil
}

var voidTags = words("area base br col embed hr img input link meta param source track wbr")

// The escape replacers are immutable and goroutine-safe, so one instance
// serves every call; the ContainsAny guard returns strings without special
// bytes unchanged, which is what the replacer would produce. Output bytes are
// identical to the per-call NewReplacer the previous code built on every
// invocation.
var (
	escapeTextReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\u00a0", "&nbsp;")
)

// escapeText is the context-free text escaper used by StripTags; the
// pipeline's own serializers write escapes straight into the arena.
func escapeText(s string) string {
	if !strings.ContainsAny(s, "&<>\u00a0") {
		return s
	}
	return escapeTextReplacer.Replace(s)
}

// serialize renders n into the arena and returns the view. The result is
// valid until the owning arena's next Reset; output-level strings must be
// copied (strings.Clone) before the arena is released.
func serialize(a *xhtml.Arena, n *xhtml.Node) string {
	b := a.NewBuilder()
	serializeTo(&b, n, false, false)
	return b.String()
}
func serializeTo(b *xhtml.Builder, n *xhtml.Node, raw, attributeAngles bool) {
	switch n.Type {
	case xhtml.TextNode:
		if raw {
			b.WriteString(n.Data)
		} else {
			escapeTextTo(b, n.Data)
		}
		return
	case xhtml.CommentNode:
		b.WriteString("<!--")
		b.WriteString(n.Data)
		b.WriteString("-->")
		return
	case xhtml.ElementNode:
		b.PutByte('<')
		b.WriteString(n.Data)
		for _, a := range n.Attr {
			b.PutByte(' ')
			if a.Namespace != "" {
				b.WriteString(a.Namespace)
				b.PutByte(':')
			}
			b.WriteString(a.Key)
			b.WriteString(`="`)
			if attributeAngles {
				escapeAttrAnglesTo(b, a.Val)
			} else {
				escapeAttrTo(b, a.Val)
			}
			b.PutByte('"')
		}
		b.PutByte('>')
		if n.Namespace == "" && voidTags[n.Data] {
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		serializeTo(b, c, n.Namespace == "" && rawTags[n.Data], attributeAngles)
	}
	if n.Type == xhtml.ElementNode {
		b.WriteString("</")
		b.WriteString(n.Data)
		b.PutByte('>')
	}
}

// escapeTextTo appends the escapeText escaping of s (text-node rules).
func escapeTextTo(b *xhtml.Builder, s string) {
	last := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '&':
			esc = "&amp;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case 0xc2: // \u00a0 in UTF-8 is c2 a0
			if i+1 < len(s) && s[i+1] == 0xa0 {
				b.WriteString(s[last:i])
				b.WriteString("&nbsp;")
				i++
				last = i + 1
			}
		default:
			continue
		}
		if esc != "" {
			b.WriteString(s[last:i])
			b.WriteString(esc)
			last = i + 1
		}
	}
	b.WriteString(s[last:])
}

// escapeAttrTo appends the escapeAttr escaping of s (attribute rules: &, ",
// nbsp).
func escapeAttrTo(b *xhtml.Builder, s string) {
	last := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '&':
			esc = "&amp;"
		case '"':
			esc = "&quot;"
		case 0xc2:
			if i+1 < len(s) && s[i+1] == 0xa0 {
				b.WriteString(s[last:i])
				b.WriteString("&nbsp;")
				i++
				last = i + 1
			}
		default:
			continue
		}
		if esc != "" {
			b.WriteString(s[last:i])
			b.WriteString(esc)
			last = i + 1
		}
	}
	b.WriteString(s[last:])
}

// escapeAttrAnglesTo is serializePresentation's attribute escaping: the
// attribute rules, then the angle-bracket pass the presentation serializer
// applies on top (a literal < becomes &amp;lt; — the two replacers compose).
func escapeAttrAnglesTo(b *xhtml.Builder, s string) {
	last := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '&':
			esc = "&amp;"
		case '"':
			esc = "&quot;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case 0xc2:
			if i+1 < len(s) && s[i+1] == 0xa0 {
				b.WriteString(s[last:i])
				b.WriteString("&nbsp;")
				i++
				last = i + 1
			}
		default:
			continue
		}
		if esc != "" {
			b.WriteString(s[last:i])
			b.WriteString(esc)
			last = i + 1
		}
	}
	b.WriteString(s[last:])
}
func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func serializePresentation(a *xhtml.Arena, n *xhtml.Node) string {
	b := a.NewBuilder()
	serializeTo(&b, n, false, true)
	return b.String()
}

var rawTags = words("style script xmp iframe noembed noframes plaintext noscript")
