package richtext

import (
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
	"golang.org/x/net/html/atom"
)

func parse(body string) (*xhtml.Node, error) {
	return parseIn(body, nil)
}
func parseIn(body string, context *xhtml.Node) (*xhtml.Node, error) {
	if context == nil || context.Type != xhtml.ElementNode {
		context = &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	} else {
		context = &xhtml.Node{Type: xhtml.ElementNode, Data: context.Data, DataAtom: context.DataAtom, Namespace: context.Namespace}
	}
	nodes, err := xhtml.ParseFragmentWithOptions(strings.NewReader(strings.TrimPrefix(body, "\ufeff")), context, xhtml.ParseOptionEnableScripting(false))
	if err != nil {
		return nil, err
	}
	root := &xhtml.Node{Type: xhtml.DocumentNode}
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
func setAttr(n *xhtml.Node, key, value string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, xhtml.Attribute{Key: key, Val: value})
}
func children(n *xhtml.Node) []*xhtml.Node {
	var out []*xhtml.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, c)
	}
	return out
}
func walk(n *xhtml.Node, fn func(*xhtml.Node)) {
	for _, c := range children(n) {
		walk(c, fn)
	}
	fn(n)
}
func clone(n *xhtml.Node) *xhtml.Node {
	out := &xhtml.Node{Type: n.Type, Data: n.Data, DataAtom: n.DataAtom, Namespace: n.Namespace, Attr: append([]xhtml.Attribute(nil), n.Attr...)}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out.AppendChild(clone(c))
	}
	return out
}
func replace(n *xhtml.Node, markup string) error {
	root, err := parseIn(markup, n.Parent)
	if err != nil {
		return err
	}
	if n.Parent == nil {
		return nil
	}
	for _, c := range children(root) {
		root.RemoveChild(c)
		n.Parent.InsertBefore(c, n)
	}
	n.Parent.RemoveChild(n)
	return nil
}
func inner(n *xhtml.Node, markup string) error {
	root, err := parseIn(markup, n)
	if err != nil {
		return err
	}
	for _, c := range children(n) {
		n.RemoveChild(c)
	}
	for _, c := range children(root) {
		root.RemoveChild(c)
		n.AppendChild(c)
	}
	return nil
}

var voidTags = words("area base br col embed hr img input link meta param source track wbr")

var (
	textEscaper      = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\u00a0", "&nbsp;")
	attributeEscaper = strings.NewReplacer("&", "&amp;", "\"", "&quot;", "\u00a0", "&nbsp;")
	angleEscaper     = strings.NewReplacer("<", "&lt;", ">", "&gt;")
)

func escapeText(s string) string { return textEscaper.Replace(s) }
func escapeAttr(s string) string { return attributeEscaper.Replace(s) }
func serialize(n *xhtml.Node) string {
	var b strings.Builder
	serializeTo(&b, n, false, false)
	return b.String()
}
func serializeTo(b *strings.Builder, n *xhtml.Node, raw, attributeAngles bool) {
	switch n.Type {
	case xhtml.TextNode:
		if raw {
			b.WriteString(n.Data)
		} else {
			b.WriteString(escapeText(n.Data))
		}
		return
	case xhtml.CommentNode:
		b.WriteString("<!--" + n.Data + "-->")
		return
	case xhtml.ElementNode:
		b.WriteByte('<')
		b.WriteString(n.Data)
		for _, a := range n.Attr {
			b.WriteByte(' ')
			if a.Namespace != "" {
				b.WriteString(a.Namespace + ":")
			}
			b.WriteString(a.Key)
			b.WriteString(`="`)
			value := escapeAttr(a.Val)
			if attributeAngles {
				value = angleEscaper.Replace(value)
			}
			b.WriteString(value)
			b.WriteByte('"')
		}
		b.WriteByte('>')
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
		b.WriteByte('>')
	}
}
func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func serializePresentation(n *xhtml.Node) string {
	var b strings.Builder
	serializeTo(&b, n, false, true)
	return b.String()
}

var rawTags = words("style script xmp iframe noembed noframes plaintext noscript")
