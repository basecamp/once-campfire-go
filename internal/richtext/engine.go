package richtext

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

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

// arenaFreelist hands out per-call arena slabs: the whole rich-text pipeline
// for one message — parse trees, serializations, plain text, presentation
// building — lives in one arena, which is reset and returned here when the
// call ends. A mutex-guarded freelist is used instead of sync.Pool
// deliberately: pool entries are purged on every GC cycle, which under the
// per-message output allocation pressure would rebuild the slabs on almost
// every call. The freelist retains a bounded set of arenas per process; the
// contract is one goroutine per arena at a time and exactly one return
// (defer at the entry point). Output strings (Result fields, public
// returns) are copied out of the arena before release; no value that
// outlives the call may reference arena memory.
var arenaFreelist struct {
	sync.Mutex
	slabs []*xhtml.Arena
}

const maxArenas = 8

func acquireArena() *xhtml.Arena {
	arenaFreelist.Lock()
	if n := len(arenaFreelist.slabs); n > 0 {
		a := arenaFreelist.slabs[n-1]
		arenaFreelist.slabs = arenaFreelist.slabs[:n-1]
		arenaFreelist.Unlock()
		return a
	}
	arenaFreelist.Unlock()
	return xhtml.NewArena()
}

func releaseArena(a *xhtml.Arena) {
	a.Reset()
	arenaFreelist.Lock()
	if len(arenaFreelist.slabs) < maxArenas {
		arenaFreelist.slabs = append(arenaFreelist.slabs, a)
		arenaFreelist.Unlock()
		return
	}
	arenaFreelist.Unlock()
}

var attributeOrder = []string{"sgid", "content-type", "url", "href", "filename", "filesize", "width", "height", "previewable", "presentation", "caption", "content"}

func load(a *xhtml.Arena, body string) (*xhtml.Node, error) {
	root, err := parse(a, strings.Trim(body, "\x00\t\n\v\f\r "))
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
				if rubyJSON(a, attr(n, key), &parsed) == nil && parsed != nil && parsed != false {
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
						text = fmtSprint(v)
					}
					attrs = a.AttrAppend(attrs, xhtml.Attribute{Key: name, Val: text})
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
			for {
				c := n.FirstChild
				if c == nil {
					break
				}
				n.RemoveChild(c)
			}
		}
	})
	if failure != nil {
		return nil, failure
	}
	galleries(a, root, false)
	return root, nil
}

// fmtSprint mirrors the reference's fmt.Sprint formatting for the
// non-string attribute value forms the Trix JSON decodes into (numbers and
// booleans): %v of float64 formats integral values without a decimal point
// and switches to exponent notation at the same thresholds the reference
// hits — the calls are byte-identical by virtue of using fmt itself.
func fmtSprint(v any) string { return fmt.Sprintf("%v", v) }

// PlainText follows the same attachment and whitespace rules as Process without
// rendering HTML variants that callers storing the search text do not need.
func PlainText(body string, ctx Context) (string, error) {
	a := acquireArena()
	defer releaseArena(a)
	return plainText(a, body, ctx)
}
func plainText(a *xhtml.Arena, body string, ctx Context) (string, error) {
	root, err := load(a, body)
	if err != nil {
		return "", err
	}
	if err = replaceAttachments(a, root, ctx, true, 0); err != nil {
		return "", err
	}
	return strings.Clone(chomp(plain(a, root))), nil
}

type outputFields uint8

const (
	displayOutput outputFields = 1 << iota
	editableOutput
	bodyOutput
	mentionsOutput
)

func Process(body string, ctx Context) (Result, error) {
	a := acquireArena()
	defer releaseArena(a)
	return process(a, body, ctx, displayOutput|editableOutput|bodyOutput|mentionsOutput)
}

// Display renders message HTML and plain text without computing editor markup,
// API body HTML or mention recipients that the message template never reads.
func Display(body string, ctx Context) (Result, error) {
	a := acquireArena()
	defer releaseArena(a)
	return process(a, body, ctx, displayOutput)
}
func Editable(body string, ctx Context) (string, error) {
	a := acquireArena()
	defer releaseArena(a)
	return editable(a, body, ctx)
}
func MentionIDs(body string, ctx Context) ([]int64, error) {
	a := acquireArena()
	defer releaseArena(a)
	result, err := process(a, body, ctx, mentionsOutput)
	if err == nil {
		err = result.Errors["mentioned"]
	}
	return result.Mentioned, err
}

func process(a *xhtml.Arena, body string, ctx Context, fields outputFields) (Result, error) {
	result := Result{Mentioned: []int64{}}
	var err error
	if fields&editableOutput != 0 {
		result.Editable, err = editable(a, body, ctx)
		if err != nil {
			setResultError(&result, "editable", err)
		}
	}

	root, err := load(a, body)
	if err != nil {
		for _, field := range []string{"plain", "body_html", "filtered", "mentioned"} {
			setResultError(&result, field, err)
		}
		return result, nil
	}
	return processRoot(a, result, root, ctx, fields)
}

func setResultError(result *Result, field string, err error) {
	if result.Errors == nil {
		result.Errors = map[string]error{}
	}
	result.Errors[field] = err
}

// ProcessMessage computes the outputs the message create pipeline stores and
// renders from one load of the canonical body: Display's presentation and
// plain text, PlainText's plain text (the same value), and MentionIDs'
// recipients. For any body the three outputs equal the separate Display,
// PlainText and MentionIDs calls on the same input, including their empty
// outputs on failure, so the create path can derive everything from one
// parse instead of three.
func ProcessMessage(body string, ctx Context) (Result, error) {
	a := acquireArena()
	defer releaseArena(a)
	return processMessage(a, body, ctx)
}

// processMessage is ProcessMessage's arena-injected form.
func processMessage(a *xhtml.Arena, body string, ctx Context) (Result, error) {
	result := Result{Mentioned: []int64{}}
	root, err := load(a, body)
	if err != nil {
		for _, field := range []string{"plain", "filtered", "mentioned"} {
			setResultError(&result, field, err)
		}
		return result, nil
	}
	return processRoot(a, result, root, ctx, displayOutput|mentionsOutput)
}

// processRoot runs process's output derivation on an already-loaded tree,
// filling the tree-based fields into result (which may already carry the
// string-based editable output). process never reads the original body
// string after load — every later step works on the tree or on serialized
// forms of it — so for any body, processRoot with load(body) is exactly the
// rest of process(body, ctx, fields).
func processRoot(a *xhtml.Arena, result Result, root *xhtml.Node, ctx Context, fields outputFields) (Result, error) {
	if fields&displayOutput != 0 {
		plainRoot := clone(a, root)
		if err := replaceAttachments(a, plainRoot, ctx, true, 0); err != nil {
			setResultError(&result, "plain", err)
		} else {
			result.Plain = strings.Clone(chomp(plain(a, plainRoot)))
		}
	}

	if fields&bodyOutput != 0 {
		rendered := clone(a, root)
		if err := replaceAttachments(a, rendered, ctx, false, 0); err != nil {
			setResultError(&result, "body_html", err)
		} else {
			galleries(a, rendered, true)
			var err error
			rendered, err = parse(a, serialize(a, rendered))
			if err != nil {
				return result, err
			}
			sanitizeDOM(rendered, "action")
			result.BodyHTML = "<div class=\"lexxy-content\">\n  " + serialize(a, rendered) + "\n</div>\n"
		}
	}

	if fields&displayOutput != 0 {
		filtered := clone(a, root)
		if result.Errors["plain"] != nil {
			setResultError(&result, "filtered", result.Errors["plain"])
		} else {
			removeSoloEmbed(a, filtered, ctx, result.Plain)
			filterTags(filtered)
			sanitizeDOM(filtered, "filter")
			var err error
			filtered, err = parse(a, strings.Trim(serialize(a, filtered), "\x00\t\n\v\f\r "))
			if err != nil {
				return result, err
			}
			result.Filtered = strings.Clone(serialize(a, filtered))
			if err = replaceAttachments(a, filtered, ctx, false, 0); err == nil {
				galleries(a, filtered, true)
				filtered, err = parse(a, serialize(a, filtered))
				if err != nil {
					return result, err
				}
				sanitizeDOM(filtered, "action")
				var presentation *xhtml.Node
				presentation, err = parse(a, a.Concat3("<div class=\"lexxy-content\">\n  ", serialize(a, filtered), "\n</div>\n"))
				if err == nil {
					sanitizeDOM(presentation, "auto")
					// The reference discards autoLink's error here; keep the
					// same outputs on every input.
					linked, _ := autoLink(a, serializePresentation(a, presentation))
					result.Presentation = strings.Clone(linked)
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
func editable(a *xhtml.Arena, body string, ctx Context) (string, error) {
	root, err := parse(a, strings.Trim(body, "\x00\t\n\v\f\r "))
	if err != nil {
		return "", err
	}
	var failure error
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			root, err = parse(a, serialize(a, root))
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
			markup, ct, err := attachment(a, n, ctx, false, 0)
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
			setAttr(a, n, "content-type", ct)
			if pass == 1 {
				raw, err := json.Marshal(markup)
				if err != nil {
					failure = err
					return
				}
				markup = string(raw)
			}
			setAttr(a, n, "content", markup)
		})
		if failure != nil {
			return "", failure
		}
	}
	if strings.TrimSpace(serialize(a, root)) == "" {
		return "", nil
	}
	return strings.Clone(serialize(a, root)), nil
}
func removeSoloEmbed(a *xhtml.Arena, root *xhtml.Node, ctx Context, text string) {
	var embeds []*xhtml.Node
	walk(root, func(n *xhtml.Node) {
		if n.Data == "action-text-attachment" && opengraphType.MatchString(attr(n, "content-type")) {
			embeds = append(embeds, n)
		}
	})
	if len(embeds) != 1 {
		return
	}
	markup, _ := embedHTML(a, embeds[0], ctx)
	parsed, err := parse(a, markup)
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
		attachment := serialize(a, embeds[0])
		for _, div := range divs {
			inner(a, div, attachment)
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

func replaceAttachments(a *xhtml.Arena, root *xhtml.Node, ctx Context, asPlain bool, depth int) error {
	var failure error
	walk(root, func(n *xhtml.Node) {
		if failure != nil || n.Data != "action-text-attachment" || n.Parent == nil {
			return
		}
		if value := attr(n, "content"); value != "" {
			content, err := parse(a, value)
			if err != nil {
				failure = err
				return
			}
			sanitizeDOM(content, "action")
			sanitized := serialize(a, content)
			removeAttr(n, "content")
			if strings.TrimSpace(sanitized) != "" {
				setAttr(a, n, "content", sanitized)
			}
		}
		markup, _, err := attachment(a, n, ctx, asPlain, depth)
		if err != nil {
			failure = err
			return
		}
		if asPlain {
			failure = replace(a, n, markup)
			return
		}
		attrs := []xhtml.Attribute{}
		for _, key := range attributeOrder {
			for _, at := range n.Attr {
				if at.Key == key {
					attrs = a.AttrAppend(attrs, at)
					break
				}
			}
		}
		if len(attrs) == 0 {
			failure = errors.New("missing attachment attributes")
			return
		}
		full := a.AllocNode(xhtml.ElementNode, 0, "action-text-attachment", "", attrs)
		failure = inner(a, full, markup)
		if failure == nil {
			failure = replace(a, n, serialize(a, full))
		}
	})
	return failure
}
func attachment(a *xhtml.Arena, n *xhtml.Node, ctx Context, asPlain bool, depth int) (string, string, error) {
	ct := attr(n, "content-type")
	caption := attr(n, "caption")
	if opengraphType.MatchString(ct) {
		markup, err := embedHTML(a, n, ctx)
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
			setAttr(a, n, "content-type", ct)
			if asPlain {
				return "@" + user.Name, ct, nil
			}
			return strings.TrimSuffix(mentionHTML(a, *user), "\n"), ct, nil
		}
	}
	content := attr(n, "content")
	if strings.Contains(ct, "html") && strings.TrimSpace(content) != "" {
		if depth >= 8 {
			return "", ct, nil
		}
		nested, err := load(a, content)
		if err != nil {
			return "", "", err
		}
		if !asPlain {
			err = replaceAttachments(a, nested, ctx, false, depth+1)
		}
		if err != nil {
			return "", "", err
		}
		if asPlain {
			return serialize(a, nested), ct, nil
		}
		sanitizeDOM(nested, "action")
		return a.Concat3("<figure class=\"attachment attachment--content\">\n  ", serialize(a, nested), "\n\n</figure>"), ct, nil
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
		b := a.NewBuilder()
		sizeB := a.NewBuilder()
		for _, key := range []string{"width", "height"} {
			if value := attr(n, key); value != "" {
				sizeB.WriteString(" ")
				sizeB.WriteString(key)
				sizeB.WriteString(`="`)
				sizeB.WriteString(erbEscape(a, value))
				sizeB.WriteString(`"`)
			}
		}
		size := sizeB.String()
		if !strings.HasPrefix(src, "/") && !strings.Contains(src, "://") && !strings.HasPrefix(src, "cid:") && !strings.HasPrefix(src, "data:") {
			return "", "", errors.New("missing remote image asset")
		}
		if video {
			b.WriteString(`<figure class="attachment attachment--preview attachment--video">` + "\n  <video controls=\"controls\"")
			b.WriteString(size)
			b.WriteString(">\n    <source src=\"")
			b.WriteString(erbEscape(a, src))
			b.WriteString(`" type="`)
			b.WriteString(erbEscape(a, ct))
			b.WriteString("\">\n</video>")
		} else {
			b.WriteString(`<figure class="attachment attachment--preview">` + "\n  <img")
			b.WriteString(size)
			b.WriteString(` src="`)
			b.WriteString(erbEscape(a, src))
			b.WriteString(`" />` + "\n")
		}
		if caption != "" {
			b.WriteString("    <figcaption class=\"attachment__caption\">\n      ")
			b.WriteString(erbEscape(a, caption))
			b.WriteString("\n    </figcaption>\n")
		}
		b.WriteString("</figure>")
		return b.String(), ct, nil
	}
	if asPlain {
		return caption, ct, nil
	}
	return "☒", ct, nil
}

// MentionHTML renders a user mention span.
func MentionHTML(user Mention) string {
	a := acquireArena()
	defer releaseArena(a)
	return mentionHTML(a, user)
}

// mentionHTML is the arena-injected form; the exported wrapper clones the
// view into a real string at the boundary.
func mentionHTML(a *xhtml.Arena, user Mention) string {
	b := a.NewBuilder()
	b.WriteString(`<span class="mention" sgid="`)
	b.WriteString(erbEscape(a, user.SGID))
	b.WriteString(`"><a title="`)
	b.WriteString(erbEscape(a, user.Title))
	b.WriteString(`" class="btn avatar" data-turbo-frame="_top" href="`)
	b.WriteString(erbEscape(a, user.Path))
	b.WriteString(`"><img aria-hidden="true" src="`)
	b.WriteString(erbEscape(a, user.Avatar))
	b.WriteString(`" width="48" height="48" /></a> `)
	b.WriteString(erbEscape(a, user.Name))
	b.WriteString("</span>\n")
	return strings.Clone(b.String())
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
	if !regexp.MustCompile(`[a-zA-Z]`).MatchString(label) || strings.HasPrefix(strings.ToLower(label), "0x") || strings.EqualFold(name, strings.TrimSuffix(host, ".")) {
		return "", nil
	}
	return value, nil
}

var opengraphType = regexp.MustCompile(`application/vnd.actiontext.opengraph-embed`)

func embedHTML(a *xhtml.Arena, n *xhtml.Node, ctx Context) (string, error) {
	href, src, title, description := attr(n, "href"), attr(n, "url"), attr(n, "filename"), attr(n, "caption")
	if strings.TrimSpace(title) == "" {
		href, src, title, description = "", "", "", ""
		root, err := parse(a, attr(n, "content"))
		if err == nil {
			walk(root, func(c *xhtml.Node) {
				classes := words(attr(c, "class"))
				if classes["og-embed__title"] {
					title = strings.TrimSpace(plain(a, c))
					walk(c, func(x *xhtml.Node) {
						if x.Data == "a" {
							href = attr(x, "href")
						}
					})
				}
				if classes["og-embed__description"] {
					description = strings.TrimSpace(plain(a, c))
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
	title = erbEscape(a, truncate(title, 280))
	description = erbEscape(a, truncate(description, 560))
	b := a.NewBuilder()
	b.WriteString(`<figure class="attachment attachment--content attachment--og">` + "\n  <actiontext-opengraph-embed>\n    <div class=\"og-embed gap ")
	if strings.HasPrefix(src, "https://pbs.twimg.com/profile_images") {
		b.WriteString("og-embed--twitter-avatar")
	}
	b.WriteString("\">\n      <div class=\"og-embed__content\">\n        <div class=\"og-embed__title\">\n          ")
	if href != "" {
		if title == "" {
			title = erbEscape(a, href)
		}
		b.WriteString(`<a rel="noreferrer" target="_blank" href="`)
		b.WriteString(erbEscape(a, href))
		b.WriteString(`">`)
		b.WriteString(title)
		b.WriteString(`</a>`)
	} else {
		b.WriteString(title)
	}
	b.WriteString("\n        </div>\n        <div class=\"og-embed__description\">")
	b.WriteString(description)
	b.WriteString("</div>\n      </div>\n")
	if src != "" {
		b.WriteString("        <div class=\"og-embed__image\">\n          <img src=\"")
		b.WriteString(erbEscape(a, src))
		b.WriteString(`" class="image center" alt="">` + "\n        </div>\n")
	}
	b.WriteString("    </div>\n  </actiontext-opengraph-embed>\n</figure>")
	return b.String(), nil
}

// truncate cuts s to n runes with an ellipsis, exactly as the reference's
// rune-slice version does; the short path avoids the conversion allocation.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// erbEscape mirrors Rails' ERB::Util.html_escape. The replacer is immutable
// and shared; the ContainsAny guard returns strings without special bytes
// unchanged, which is what the replacer would produce. Escaped output lives
// in the arena.
func erbEscape(a *xhtml.Arena, s string) string {
	if !strings.ContainsAny(s, "&<>\"'") {
		return s
	}
	b := a.NewBuilder()
	escapeErbTo(&b, s)
	return b.String()
}

func escapeErbTo(b *xhtml.Builder, s string) {
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
		case '"':
			esc = "&quot;"
		case '\'':
			esc = "&#39;"
		default:
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(esc)
		last = i + 1
	}
	b.WriteString(s[last:])
}
func galleries(a *xhtml.Arena, root *xhtml.Node, render bool) {
	walk(root, func(n *xhtml.Node) {
		if n.Data != "div" {
			return
		}
		var members []*xhtml.Node
		for c := n.FirstChild; c != nil; c = c.NextSibling {
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
			b := a.NewBuilder()
			b.WriteString("attachment-gallery attachment-gallery--")
			b.WriteString(strconv.Itoa(len(members)))
			setAttr(a, n, "class", b.String())
			html := a.NewBuilder()
			html.WriteString("\n  ")
			for _, c := range members {
				html.WriteString(serialize(a, c))
			}
			html.WriteString("\n")
			inner(a, n, html.String())
		}
	})
}
func rubyJSON(a *xhtml.Arena, s string, value any) error {
	b := a.NewBuilder()
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			b.PutByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.PutByte(s[i])
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
				b.PutByte(' ')
				continue
			}
			if s[i+1] == '/' {
				for i+1 < len(s) && s[i+1] != '\n' {
					i++
				}
				b.PutByte(' ')
				continue
			}
		}
		b.PutByte(c)
	}
	return json.Unmarshal(b.Bytes(), value)
}

func removeAttr(n *xhtml.Node, key string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}
