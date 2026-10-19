package richtext

import (
	"html"
	"regexp"
	"strings"
	"unicode"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

var defaultTags = words("a abbr acronym address b big blockquote br cite code dd del dfn div dl dt em h1 h2 h3 h4 h5 h6 hr i img ins kbd li mark ol p pre samp small span strong sub sup time tt ul var")
var editorTags = words("s u mark table thead tbody tfoot tr th td")
var attachmentAttrs = words("sgid content-type url href filename filesize width height previewable presentation caption content")
var defaultAttrs = words("abbr alt cite class datetime height href lang src title width xml:lang")
var actionAttrs = words("controls poster data-language style value start")
var actionTags = words("action-text-attachment figure figcaption video audio source embed")
var uriAttrs = words("action cite href longdesc poster preload src xlink:href xml:base")
var protocols = words("afs aim callto data ed2k fax ftp gopher http https irc line mailto modem news nntp rsync rtsp sftp sms ssh tag tel telnet urn webcal xmpp")
var dataTypes = words("image/gif image/jpeg image/png text/css text/plain")
var schemePattern = regexp.MustCompile(`^([a-z][a-z0-9+.-]*)(?::|%3a|&#0*58|&#x0*3a|&#37;3a)`)
var colorPattern = regexp.MustCompile(`(?i)^(?:[a-z]+|#[0-9a-f]{3,8}|var\(\s*--[a-z0-9_-]+\s*\)|(?:rgb|rgba|hsl|hsla)\([0-9a-z.,%\s/+-]*\))$`)

func allowedURI(value string) bool {
	strip := func(s string) string {
		return strings.Map(func(c rune) rune {
			if c == '`' || c <= 32 || c == 127 || c >= 128 && c <= 257 {
				return -1
			}
			return c
		}, s)
	}
	s := strings.ToLower(strip(html.UnescapeString(strip(value))))
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "&tab;", ""), "&newline;", ""), "&colon;", ":")
	match := schemePattern.FindStringSubmatch(s)
	if match == nil {
		return true
	}
	if !protocols[match[1]] {
		return false
	}
	if match[1] != "data" {
		return true
	}
	metadata, _, ok := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	if !ok {
		return false
	}
	media, _, _ := strings.Cut(metadata, ";")
	if !strings.Contains(media, "/") {
		media = "text/plain"
	}
	return dataTypes[media]
}
func sanitizeDOM(root *xhtml.Node, mode string) {
	walk(root, func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode || n.Parent == nil {
			return
		}
		tag := n.Data
		allowed := defaultTags[tag] || editorTags[tag]
		if mode == "action" {
			allowed = allowed || actionTags[tag]
		}
		if mode == "filter" {
			allowed = tag != "img" && (allowed || tag == "action-text-attachment" || tag == "figure" || tag == "figcaption")
		}
		if !allowed {
			if n.Namespace == "" {
				// Reparent every child before n, preserving order: popping
				// the front child and inserting it right before n keeps the
				// sequence c1..ck in the original order (each insertion
				// lands after the ones already done).
				for n.FirstChild != nil {
					c := n.FirstChild
					n.RemoveChild(c)
					n.Parent.InsertBefore(c, n)
				}
			}
			n.Parent.RemoveChild(n)
			return
		}
		for i := 0; i < len(n.Attr); {
			a := n.Attr[i]
			key := a.Key
			if a.Namespace != "" {
				key = a.Namespace + ":" + key
			}
			keep := defaultAttrs[key] || key == "data-language"
			if mode != "auto" {
				keep = keep || attachmentAttrs[key] || actionAttrs[key]
			}
			if !keep || uriAttrs[key] && !allowedURI(a.Val) {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
				continue
			}
			if key == "src" && strings.TrimFunc(a.Val, unicode.IsSpace) == "" {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			} else {
				i++
			}
			for j, a := range n.Attr {
				if a.Key == "href" || a.Key == "action" || a.Key == "src" {
					a.Val = strings.Map(func(c rune) rune {
						if c < 32 && c != '\t' && c != '\n' && c != '\r' {
							return -1
						}
						return c
					}, a.Val)
					a.Val = strings.ReplaceAll(strings.ReplaceAll(a.Val, " ", "%20"), "\"", "%22")
					n.Attr[j] = a
				}
			}
		}
		for i, a := range n.Attr {
			if a.Key != "style" {
				continue
			}
			var safe []string
			all := true
			for _, declaration := range strings.Split(a.Val, ";") {
				if strings.TrimSpace(declaration) == "" {
					continue
				}
				key, value, _ := strings.Cut(declaration, ":")
				key = strings.ToLower(strings.TrimSpace(key))
				value = strings.TrimSpace(value)
				if (key == "color" || key == "background-color") && colorPattern.MatchString(value) {
					safe = append(safe, key+": "+value+";")
				} else {
					all = false
				}
			}
			if len(safe) == 0 {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			} else if !all {
				n.Attr[i].Val = strings.Join(safe, "")
			}
			break
		}
	})
}
func filterTags(root *xhtml.Node) {
	walk(root, func(n *xhtml.Node) {
		if n.Type != xhtml.ElementNode || n.Parent == nil {
			return
		}
		keep := n.Data != "img" && (defaultTags[n.Data] || editorTags[n.Data] || n.Data == "action-text-attachment" || n.Data == "figure" || n.Data == "figcaption")
		if !keep {
			n.Parent.RemoveChild(n)
		}
	})
}
