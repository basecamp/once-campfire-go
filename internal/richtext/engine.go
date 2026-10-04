package richtext

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

var hostLetter = regexp.MustCompile(`[a-zA-Z]`)

type Mention struct {
	ID                              int64
	Name, Title, SGID, Path, Avatar string
}
type Context struct {
	Host    string
	Resolve func(sgid string, verified bool) (*Mention, error)
}
type Result struct {
	Errors                                            map[string]error
	Presentation, BodyHTML, Plain, Filtered, Editable string
	Mentioned                                         []int64
}

var attributeOrder = []string{"sgid", "content-type", "url", "href", "filename", "filesize", "width", "height", "previewable", "presentation", "caption", "content"}

func load(body string) (*xhtml.Node, error) {
	root, err := parse(strings.Trim(body, "\x00\t\n\v\f\r "))
	if err != nil {
		return nil, err
	}
	var failure error
	walk(root, func(n *xhtml.Node) {
		if failure != nil {
			return
		}
		if n.Type != xhtml.ElementNode {
			return
		}
		if attr(n, "data-trix-attachment") != "" {
			data := map[string]any{}
			for _, key := range []string{"data-trix-attachment", "data-trix-attributes"} {
				var parsed any
				if rubyJSON(attr(n, key), &parsed) == nil && parsed != nil && parsed != false {
					values, ok := parsed.(map[string]any)
					if !ok {
						failure = errors.New("missing merge on Trix attributes")
						return
					}
					for key, value := range values {
						data[key] = value
					}
				}
			}
			attrs := []xhtml.Attribute{}
			for _, name := range attributeOrder {
				key := name
				if key == "content-type" {
					key = "contentType"
				}
				if value, ok := data[key]; ok {
					var text string
					switch v := value.(type) {
					case string:
						text = v
					case nil:
						text = ""
					default:
						text = fmt.Sprint(v)
					}
					attrs = append(attrs, xhtml.Attribute{Key: name, Val: text})
				}
			}
			if len(attrs) == 0 {
				if n.Parent != nil {
					n.Parent.RemoveChild(n)
				}
				return
			}
			n.Data = "action-text-attachment"
			n.DataAtom = 0
			n.Attr = attrs
		}
		if n.Data == "action-text-attachment" {
			for _, c := range children(n) {
				n.RemoveChild(c)
			}
		}
	})
	if failure != nil {
		return nil, failure
	}
	galleries(root, false)
	return root, nil
}

// PlainText follows the same attachment and whitespace rules as Process without
// rendering HTML variants that callers storing the search text do not need.
func PlainText(body string, ctx Context) (string, error) {
	root, err := load(body)
	if err != nil {
		return "", err
	}
	if err = replaceAttachments(root, ctx, true, 0); err != nil {
		return "", err
	}
	return chomp(plain(root)), nil
}

type outputFields uint8

const (
	displayOutput outputFields = 1 << iota
	editableOutput
	bodyOutput
	mentionsOutput
)

func Process(body string, ctx Context) (Result, error) {
	return process(body, ctx, displayOutput|editableOutput|bodyOutput|mentionsOutput)
}

// Display renders message HTML and plain text without computing editor markup,
// API body HTML or mention recipients that the message template never reads.
func Display(body string, ctx Context) (Result, error) {
	return process(body, ctx, displayOutput)
}
func Editable(body string, ctx Context) (string, error) { return editable(body, ctx) }
func MentionIDs(body string, ctx Context) ([]int64, error) {
	result, err := process(body, ctx, mentionsOutput)
	if err == nil {
		err = result.Errors["mentioned"]
	}
	return result.Mentioned, err
}

func process(body string, ctx Context, fields outputFields) (Result, error) {
	result := Result{Mentioned: []int64{}, Errors: map[string]error{}}
	var err error
	if fields&editableOutput != 0 {
		result.Editable, err = editable(body, ctx)
		if err != nil {
			result.Errors["editable"] = err
		}
	}

	root, err := load(body)
	if err != nil {
		for _, field := range []string{"plain", "body_html", "filtered", "mentioned"} {
			result.Errors[field] = err
		}
		return result, nil
	}
	if fields&displayOutput != 0 {
		plainRoot := clone(root)
		if err = replaceAttachments(plainRoot, ctx, true, 0); err != nil {
			result.Errors["plain"] = err
		} else {
			result.Plain = chomp(plain(plainRoot))
		}
	}

	if fields&bodyOutput != 0 {
		rendered := clone(root)
		if err = replaceAttachments(rendered, ctx, false, 0); err != nil {
			result.Errors["body_html"] = err
		} else {
			galleries(rendered, true)
			rendered, err = parse(serialize(rendered))
			if err != nil {
				return result, err
			}
			sanitizeDOM(rendered, "action")
			result.BodyHTML = "<div class=\"lexxy-content\">\n  " + serialize(rendered) + "\n</div>\n"
		}
	}

	if fields&displayOutput != 0 {
		filtered := clone(root)
		if result.Errors["plain"] != nil {
			result.Errors["filtered"] = result.Errors["plain"]
		} else {
			removeSoloEmbed(filtered, ctx, result.Plain)
			filterTags(filtered)
			sanitizeDOM(filtered, "filter")
			filtered, err = parse(strings.Trim(serialize(filtered), "\x00\t\n\v\f\r "))
			if err != nil {
				return result, err
			}
			result.Filtered = serialize(filtered)
			if err = replaceAttachments(filtered, ctx, false, 0); err == nil {
				galleries(filtered, true)
				filtered, err = parse(serialize(filtered))
				if err != nil {
					return result, err
				}
				sanitizeDOM(filtered, "action")
				var presentation *xhtml.Node
				presentation, err = parse("<div class=\"lexxy-content\">\n  " + serialize(filtered) + "\n</div>\n")
				if err == nil {
					sanitizeDOM(presentation, "auto")
					result.Presentation, _ = autoLink(serializePresentation(presentation))
				}
			}
		}
	}

	if fields&mentionsOutput != 0 {
		walk(root, func(n *xhtml.Node) {
			if n.Data == "action-text-attachment" && ctx.Resolve != nil && attr(n, "sgid") != "" {
				if user, e := ctx.Resolve(attr(n, "sgid"), true); e == nil && user != nil {
					found := false
					for _, id := range result.Mentioned {
						if id == user.ID {
							found = true
						}
					}
					if !found {
						result.Mentioned = append(result.Mentioned, user.ID)
					}
				}
			}
		})
	}

	return result, nil
}
func editable(body string, ctx Context) (string, error) {
	root, err := parse(strings.Trim(body, "\x00\t\n\v\f\r "))
	if err != nil {
		return "", err
	}
	var failure error
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			root, err = parse(serialize(root))
			if err != nil {
				return "", err
			}
		}
		walk(root, func(n *xhtml.Node) {
			if failure != nil || n.Data != "action-text-attachment" || n.Parent == nil {
				return
			}
			if pass == 1 && strings.TrimSpace(attr(n, "url")) != "" {
				return
			}
			markup, ct, err := attachment(n, ctx, false, 0)
			if err != nil {
				failure = err
				return
			}
			if markup == "☒" {
				n.Parent.RemoveChild(n)
				return
			}
			if ct != "application/vnd.campfire.mention" && !opengraphType.MatchString(ct) {
				failure = errors.New("missing attachable_content_type")
				return
			}
			setAttr(n, "content-type", ct)
			if pass == 1 {
				raw, err := json.Marshal(markup)
				if err != nil {
					failure = err
					return
				}
				markup = string(raw)
			}
			setAttr(n, "content", markup)
		})
		if failure != nil {
			return "", failure
		}
	}
	if strings.TrimSpace(serialize(root)) == "" {
		return "", nil
	}
	return serialize(root), nil
}
func removeSoloEmbed(root *xhtml.Node, ctx Context, text string) {
	var embeds []*xhtml.Node
	walk(root, func(n *xhtml.Node) {
		if n.Data == "action-text-attachment" && opengraphType.MatchString(attr(n, "content-type")) {
			embeds = append(embeds, n)
		}
	})
	if len(embeds) != 1 {
		return
	}
	markup, _ := embedHTML(embeds[0], ctx)
	parsed, err := parse(markup)
	if err != nil {
		return
	}
	href := ""
	walk(parsed, func(n *xhtml.Node) {
		if n.Data == "a" {
			href = attr(n, "href")
		}
	})
	if href == "" {
		return
	}
	normalize := func(value string) string {
		if !strings.Contains(value, "x.com") && !strings.Contains(value, "twitter.com") {
			return value
		}
		u, err := url.Parse(value)
		if err != nil {
			return value
		}
		if strings.EqualFold(u.Hostname(), "x.com") {
			u.Host = "twitter.com"
		}
		u.RawQuery = ""
		return u.String()
	}
	if normalize(href) != normalize(text) {
		return
	}
	var divs []*xhtml.Node
	walk(root, func(n *xhtml.Node) {
		if n.Data == "div" {
			divs = append(divs, n)
		}
	})
	if len(divs) > 0 {
		attachment := serialize(embeds[0])
		for _, div := range divs {
			inner(div, attachment)
		}
		return
	}
	walk(root, func(n *xhtml.Node) {
		if n.Data != "p" || n.Parent == nil {
			return
		}
		has := false
		walk(n, func(child *xhtml.Node) {
			if child.Data == "action-text-attachment" {
				has = true
			}
		})
		if !has {
			n.Parent.RemoveChild(n)
		}
	})
}

func replaceAttachments(root *xhtml.Node, ctx Context, asPlain bool, depth int) error {
	var failure error
	walk(root, func(n *xhtml.Node) {
		if failure != nil || n.Data != "action-text-attachment" || n.Parent == nil {
			return
		}
		if value := attr(n, "content"); value != "" {
			content, err := parse(value)
			if err != nil {
				failure = err
				return
			}
			sanitizeDOM(content, "action")
			sanitized := serialize(content)
			removeAttr(n, "content")
			if strings.TrimSpace(sanitized) != "" {
				setAttr(n, "content", sanitized)
			}
		}
		markup, _, err := attachment(n, ctx, asPlain, depth)
		if err != nil {
			failure = err
			return
		}
		if asPlain {
			failure = replace(n, markup)
			return
		}
		attrs := []xhtml.Attribute{}
		for _, key := range attributeOrder {
			for _, a := range n.Attr {
				if a.Key == key {
					attrs = append(attrs, a)
					break
				}
			}
		}
		if len(attrs) == 0 {
			failure = errors.New("missing attachment attributes")
			return
		}
		full := &xhtml.Node{Type: xhtml.ElementNode, Data: "action-text-attachment", Attr: attrs}
		failure = inner(full, markup)
		if failure == nil {
			failure = replace(n, serialize(full))
		}
	})
	return failure
}
func attachment(n *xhtml.Node, ctx Context, asPlain bool, depth int) (string, string, error) {
	ct := attr(n, "content-type")
	caption := attr(n, "caption")
	if opengraphType.MatchString(ct) {
		markup, err := embedHTML(n, ctx)
		if asPlain {
			markup = ""
		}
		return markup, "application/vnd.actiontext.opengraph-embed", err
	}
	if sgid := attr(n, "sgid"); sgid != "" && ctx.Resolve != nil {
		user, err := ctx.Resolve(sgid, false)
		if err != nil {
			return "", "", err
		}
		if user != nil {
			ct = "application/vnd.campfire.mention"
			setAttr(n, "content-type", ct)
			if asPlain {
				return "@" + user.Name, ct, nil
			}
			return strings.TrimSuffix(MentionHTML(*user), "\n"), ct, nil
		}
	}
	content := attr(n, "content")
	if strings.Contains(ct, "html") && strings.TrimSpace(content) != "" {
		if depth >= 8 {
			return "", ct, nil
		}
		nested, err := load(content)
		if err != nil {
			return "", "", err
		}
		if !asPlain {
			err = replaceAttachments(nested, ctx, false, depth+1)
		}
		if err != nil {
			return "", "", err
		}
		if asPlain {
			return serialize(nested), ct, nil
		}
		sanitizeDOM(nested, "action")
		return "<figure class=\"attachment attachment--content\">\n  " + serialize(nested) + "\n\n</figure>", ct, nil
	}
	if src := attr(n, "url"); src != "" && (strings.HasPrefix(ct, "image/") || ct == "image" || strings.HasPrefix(ct, "video/") || ct == "video") {
		video := strings.HasPrefix(ct, "video")
		if asPlain {
			label := caption
			if label == "" {
				if video {
					label = attr(n, "filename")
					if label == "" {
						label = "Video"
					}
				} else {
					label = "Image"
				}
			}
			return "[" + label + "]", ct, nil
		}
		size := ""
		for _, key := range []string{"width", "height"} {
			if value := attr(n, key); value != "" {
				size += " " + key + `="` + erbEscape(value) + `"`
			}
		}
		if !strings.HasPrefix(src, "/") && !strings.Contains(src, "://") && !strings.HasPrefix(src, "cid:") && !strings.HasPrefix(src, "data:") {
			return "", "", errors.New("missing remote image asset")
		}
		var result string
		if video {
			result = `<figure class="attachment attachment--preview attachment--video">` + "\n  <video controls=\"controls\"" + size + ">\n    <source src=\"" + erbEscape(src) + "\" type=\"" + erbEscape(ct) + "\">\n</video>"
		} else {
			result = `<figure class="attachment attachment--preview">` + "\n  <img" + size + ` src="` + erbEscape(src) + `" />` + "\n"
		}
		if caption != "" {
			result += "    <figcaption class=\"attachment__caption\">\n      " + erbEscape(caption) + "\n    </figcaption>\n"
		}
		return result + "</figure>", ct, nil
	}
	if asPlain {
		return caption, ct, nil
	}
	return "☒", ct, nil
}
func MentionHTML(user Mention) string {
	return fmt.Sprintf("<span class=\"mention\" sgid=\"%s\"><a title=\"%s\" class=\"btn avatar\" data-turbo-frame=\"_top\" href=\"%s\"><img aria-hidden=\"true\" src=\"%s\" width=\"48\" height=\"48\" /></a> %s</span>\n", erbEscape(user.SGID), erbEscape(user.Title), erbEscape(user.Path), erbEscape(user.Avatar), erbEscape(user.Name))
}
func externalURL(value, host string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	if strings.ContainsAny(strings.SplitN(value, "?", 2)[0], "\"<> \t\r\n") {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", nil
	}
	if parsed.Scheme == "mailto" && !strings.Contains(parsed.Opaque, "@") {
		return "", errors.New("URI invalid component")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil
	}
	name := parsed.Hostname()
	if name == "" || strings.Contains(name, "%") || !strings.Contains(name, ".") {
		return "", nil
	}
	name = strings.TrimRight(name, ".")
	if name == "" {
		return "", errors.New("missing host label")
	}
	label := name[strings.LastIndex(name, ".")+1:]
	if !hostLetter.MatchString(label) || strings.HasPrefix(strings.ToLower(label), "0x") || strings.EqualFold(name, strings.TrimSuffix(host, ".")) {
		return "", nil
	}
	return value, nil
}

var opengraphType = regexp.MustCompile(`application/vnd.actiontext.opengraph-embed`)

func embedHTML(n *xhtml.Node, ctx Context) (string, error) {
	href, src, title, description := attr(n, "href"), attr(n, "url"), attr(n, "filename"), attr(n, "caption")
	if strings.TrimSpace(title) == "" {
		href, src, title, description = "", "", "", ""
		root, err := parse(attr(n, "content"))
		if err == nil {
			walk(root, func(c *xhtml.Node) {
				classes := words(attr(c, "class"))
				if classes["og-embed__title"] {
					title = strings.TrimSpace(plain(c))
					walk(c, func(a *xhtml.Node) {
						if a.Data == "a" {
							href = attr(a, "href")
						}
					})
				}
				if classes["og-embed__description"] {
					description = strings.TrimSpace(plain(c))
				}
				if classes["og-embed__image"] {
					walk(c, func(img *xhtml.Node) {
						if img.Data == "img" {
							src = attr(img, "src")
						}
					})
				}
			})
		}
	}
	var err error
	href, err = externalURL(href, ctx.Host)
	if err != nil {
		return "", err
	}
	src, err = externalURL(src, ctx.Host)
	if err != nil {
		return "", err
	}
	truncate := func(s string, n int) string {
		r := []rune(s)
		if len(r) > n {
			return string(r[:n-1]) + "…"
		}
		return s
	}
	title = erbEscape(truncate(title, 280))
	description = erbEscape(truncate(description, 560))
	if href != "" {
		if title == "" {
			title = erbEscape(href)
		}
		title = `<a rel="noreferrer" target="_blank" href="` + erbEscape(href) + `">` + title + `</a>`
	}
	avatarClass := ""
	if strings.HasPrefix(src, "https://pbs.twimg.com/profile_images") {
		avatarClass = "og-embed--twitter-avatar"
	}
	result := "<figure class=\"attachment attachment--content attachment--og\">\n  <actiontext-opengraph-embed>\n    <div class=\"og-embed gap " + avatarClass + "\">\n      <div class=\"og-embed__content\">\n        <div class=\"og-embed__title\">\n          " + title + "\n        </div>\n        <div class=\"og-embed__description\">" + description + "</div>\n      </div>\n"
	if src != "" {
		result += "        <div class=\"og-embed__image\">\n          <img src=\"" + erbEscape(src) + "\" class=\"image center\" alt=\"\">\n        </div>\n"
	}
	return result + "    </div>\n  </actiontext-opengraph-embed>\n</figure>", nil
}

func erbEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;").Replace(s)
}
func galleries(root *xhtml.Node, render bool) {
	walk(root, func(n *xhtml.Node) {
		if n.Data != "div" {
			return
		}
		var members []*xhtml.Node
		for _, c := range children(n) {
			if c.Type == xhtml.TextNode && strings.Trim(c.Data, "\n ") == "" {
				continue
			}
			if c.Data != "action-text-attachment" || attr(c, "presentation") != "gallery" {
				return
			}
			members = append(members, c)
		}
		if len(members) < 2 {
			return
		}
		n.Attr = nil
		if render {
			setAttr(n, "class", fmt.Sprintf("attachment-gallery attachment-gallery--%d", len(members)))
			var html strings.Builder
			html.WriteString("\n  ")
			for _, c := range members {
				html.WriteString(serialize(c))
			}
			html.WriteString("\n")
			inner(n, html.String())
		}
	})
}
func rubyJSON(s string, value any) error {
	var out strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				out.WriteByte(s[i])
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
		}
		if c == '/' && i+1 < len(s) {
			if s[i+1] == '*' {
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return errors.New("unterminated JSON comment")
				}
				i += end + 3
				out.WriteByte(' ')
				continue
			}
			if s[i+1] == '/' {
				for i+1 < len(s) && s[i+1] != '\n' {
					i++
				}
				out.WriteByte(' ')
				continue
			}
		}
		out.WriteByte(c)
	}
	return json.Unmarshal([]byte(out.String()), value)
}

func removeAttr(n *xhtml.Node, key string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}
