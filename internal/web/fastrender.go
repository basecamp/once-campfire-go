package web

// Compiled message-fragment renderer (ENGINE-32, fastrender).
//
// messageViews renders every uncached message through html/template's
// "message-uncached" fragment — measured at 25.2% of post_message CPU, 12.9%
// of it reflect.Value.call inside the template engine (bench/results/
// profile-20261006). This file compiles that fragment — and the partials it
// actually includes: message-actions, presentation, boosts and boost — once
// at startup into a flat program of literal and field ops, then renders into
// a caller-supplied buffer with allocations of its own: zero (the only
// allocations remaining on the path are the ones the template funcs
// themselves perform, e.g. the avatar signed id).
//
// Byte-identical output by construction: at compile time the fragment
// templates are first escaped by html/template itself (via a throwaway
// execution with an empty view — html/template escapes lazily on first
// execute), and the compiler reads the escaped pipelines off the template
// trees — {{.ClientID}} becomes {{.ClientID | _html_template_attrescaper}}.
// Those pipelines name exactly the escapers this renderer re-implements as
// byte-level appends (the quoted-attribute/text tables and the URL
// filter/normalizer from html/template's html.go and url.go; the only HTML
// context present is the pre-sanitized {{.HTML}} body, inserted raw). Any
// construct the compiler does not know (an added func, a changed context)
// makes it fail at startup; the server then logs and falls back to the
// html/template path, so a template edit can never silently change output.
//
// CAMPFIRE_FAST_RENDER=off (default on; same value shapes as the other
// engine flags) reverts messageViews to executing "message-uncached" through
// html/template, byte-identically.

import (
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"text/template/parse"

	"github.com/basecamp/once-campfire-go/assets"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// parseFastRender maps a CAMPFIRE_FAST_RENDER value to its setting, accepting
// the same shapes as parseRecordedPieces. An unrecognised value reports
// valid=false so the caller can warn while keeping the default on rather than
// silently changing behaviour.
func parseFastRender(raw string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return true, false
	}
}

// escapeMessageFragments triggers html/template's lazy escape pass for the
// message fragment templates so the compiler can read their escaped
// pipelines. Executing with an empty view renders nothing of interest and
// propagates any escape error upward.
func escapeMessageFragments(t *template.Template) error {
	if err := t.ExecuteTemplate(io.Discard, "message-uncached", &messageView{}); err != nil {
		return fmt.Errorf("escaping message fragments: %w", err)
	}
	return nil
}

// ---- escaping primitives: byte-identical to html/template ----

// htmlEscapeTable mirrors html/template's htmlReplacementTable (html.go),
// which _html_template_attrescaper applies to quoted attribute values and
// _html_template_htmlescaper to text. Every entry is a single-byte rune, so
// the stdlib's rune loop is exactly a byte loop; the escaper never touches
// noncharacters (badRunes=true path).
var htmlEscapeTable = [99]string{
	0:    "\uFFFD",
	'"':  "&#34;",
	'&':  "&amp;",
	'\'': "&#39;",
	'+':  "&#43;",
	'<':  "&lt;",
	'>':  "&gt;",
}

// appendEscaped writes s escaped for a quoted attribute or text node.
func appendEscaped(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 99 && htmlEscapeTable[c] != "" {
			dst = append(dst, htmlEscapeTable[c]...)
		} else {
			dst = append(dst, c)
		}
	}
	return dst
}

// filterFailsafe mirrors html/template's urlFilter failure value.
const filterFailsafe = "#ZgotmplZ"

// isSafeURL mirrors html/template's isSafeURL (url.go): relative URLs and
// http/https/mailto URLs pass, any other scheme fails.
func isSafeURL(s string) bool {
	if protocol, _, ok := strings.Cut(s, ":"); ok && !strings.Contains(protocol, "/") {
		if !strings.EqualFold(protocol, "http") && !strings.EqualFold(protocol, "https") && !strings.EqualFold(protocol, "mailto") {
			return false
		}
	}
	return true
}

// appendURLAttr writes s URL-normalized and then escaped for a quoted
// attribute, exactly as html/template's _html_template_urlnormalizer
// followed by _html_template_attrescaper would (url.go processURLOnto with
// norm=true, then html.go attrEscaper). The two stages compose into one
// pass: after normalization the attr escaper can only ever fire on '&'
// (→&amp;) and '+' (→&#43;); every other attr special is percent-encoded by
// the normalizer first.
func appendURLAttr(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '&':
			dst = append(dst, "&amp;"...)
		case c == '+':
			dst = append(dst, "&#43;"...)
		case c == '%' && i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2]):
			// Valid escapes are preserved when normalizing.
			dst = append(dst, c, s[i+1], s[i+2])
			i += 2
		case isURLKept(c):
			dst = append(dst, c)
		case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9':
			dst = append(dst, c)
		default:
			dst = appendPercentByte(dst, c)
		}
	}
	return dst
}

// isURLKept reports whether c survives URL normalization unescaped. The
// RFC 3986 reserved/sub-delim set from url.go's processURLOnto, minus '&'
// and '+' which the attribute stage handles.
func isURLKept(c byte) bool {
	switch c {
	case '!', '#', '$', '*', ',', '/', ':', ';', '=', '?', '@', '[', ']', '-', '.', '_', '~':
		return true
	}
	return false
}

func appendPercentByte(dst []byte, c byte) []byte {
	const hex = "0123456789abcdef"
	return append(dst, '%', hex[c>>4], hex[c&15])
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// ---- compiled program ----

// baseKind says which struct the ops of a program read.
type baseKind uint8

const (
	baseMessage baseKind = iota
	baseBoost
	baseReaction
)

// opKind is one instruction of the compiled fragment.
type opKind uint8

const (
	opText           opKind = iota // literal bytes
	opStrAttr                      // string field, attribute-escaped
	opStrText                      // string field, text-escaped
	opStrURL                       // string field, whole URL: filter + normalize + attr
	opStrURLPre                    // string field, inside a URL path: normalize + attr
	opInt                          // int64 field (digits need no escaping)
	opHTMLRaw                      // template.HTML field (pre-sanitized, raw)
	opEpochA                       // epoch .CreatedAt (attr context)
	opEpochU                       // epoch .UpdatedAt (attr context)
	opISO                          // iso .CreatedAt (attr context)
	opAvatarMsg                    // avatar .CreatorID .CreatorUpdatedAt, whole URL
	opAvatarBoost                  // avatar .BoosterID .BoosterUpdatedAt, whole URL
	opReactionBody                 // the current reaction's pre-rendered form body (message-actions' {{.}})
	opCond                         // branch on a field predicate
	opRangeBoost                   // iterate .Boosts
	opRangeReactions               // iterate the fixed reactions table
)

// condKind identifies the predicate of an opCond.
type condKind uint8

const (
	condAllEmoji condKind = iota
	condAttachment
	condContentAllEmoji
)

type fieldRef struct {
	base baseKind
	path string // dotted path, e.g. "Attachment.Filename"
}

type renderOp struct {
	kind       opKind
	text       string     // opText
	f          fieldRef   // opStr*/opInt/opHTMLRaw
	cond       condKind   // opCond
	sub, other []renderOp // opCond branches / opRange body and else
}

type messageRenderer struct {
	root   []renderOp
	avatar func(int64, time.Time) string
}

// render appends the message fragment (the byte-identical equivalent of
// html/template executing "message-uncached" with v) to dst and returns the
// extended slice. It performs no allocations of its own.
func (r *messageRenderer) render(dst []byte, v *messageView) []byte {
	return r.renderOps(dst, r.root, v, nil, nil)
}

func (r *messageRenderer) renderOps(dst []byte, ops []renderOp, v *messageView, b *database.Boost, rc *reaction) []byte {
	for i := range ops {
		op := &ops[i]
		switch op.kind {
		case opText:
			dst = append(dst, op.text...)
		case opStrAttr, opStrText:
			dst = appendEscaped(dst, stringField(op.f, v, b, rc))
		case opStrURL:
			s := stringField(op.f, v, b, rc)
			if !isSafeURL(s) {
				dst = append(dst, filterFailsafe...)
			} else {
				dst = appendURLAttr(dst, s)
			}
		case opStrURLPre:
			dst = appendURLAttr(dst, stringField(op.f, v, b, rc))
		case opInt:
			dst = strconv.AppendInt(dst, intField(op.f, v, b), 10)
		case opHTMLRaw:
			dst = append(dst, htmlField(op.f, v)...)
		case opEpochA:
			dst = strconv.AppendInt(dst, v.CreatedAt.UnixMilli(), 10)
		case opEpochU:
			dst = strconv.AppendInt(dst, v.UpdatedAt.UnixMilli(), 10)
		case opISO:
			dst = v.CreatedAt.UTC().AppendFormat(dst, "2006-01-02T15:04:05.000Z")
		case opAvatarMsg:
			dst = appendURLAttr(dst, r.avatar(v.CreatorID, v.CreatorUpdatedAt))
		case opAvatarBoost:
			dst = appendURLAttr(dst, r.avatar(b.BoosterID, b.BoosterUpdatedAt))
		case opReactionBody:
			// message-actions prints {{.}} over the pre-rendered body; the
			// reaction-body template produced the bytes and htmlescaper is the
			// identity on template.HTML, so they append raw.
			dst = append(dst, rc.body...)
		case opCond:
			sub := op.other
			switch op.cond {
			case condAllEmoji:
				if v.AllEmoji {
					sub = op.sub
				}
			case condAttachment:
				if v.Attachment != nil {
					sub = op.sub
				}
			case condContentAllEmoji:
				if allEmoji(b.Content) {
					sub = op.sub
				}
			}
			if len(sub) > 0 {
				dst = r.renderOps(dst, sub, v, b, rc)
			}
		case opRangeBoost:
			sub := op.other
			if len(v.Boosts) > 0 {
				sub = op.sub
				for i := range v.Boosts {
					dst = r.renderOps(dst, sub, v, &v.Boosts[i], nil)
				}
				continue
			}
			if len(sub) > 0 {
				dst = r.renderOps(dst, sub, v, b, rc)
			}
		case opRangeReactions:
			for i := range reactions {
				dst = r.renderOps(dst, op.sub, v, b, &reactions[i])
			}
		default:
			panic("fastrender: unhandled op " + strconv.Itoa(int(op.kind)))
		}
	}
	return dst
}

func stringField(f fieldRef, v *messageView, b *database.Boost, rc *reaction) string {
	switch f.base {
	case baseMessage:
		switch f.path {
		case "ClientID":
			return v.ClientID
		case "CreatorTitle":
			return v.CreatorTitle
		case "Creator":
			return v.Creator
		case "RoomName":
			return v.RoomName
		case "Permalink":
			return v.Permalink
		case "BlobURL":
			return v.BlobURL
		case "DownloadURL":
			return v.DownloadURL
		case "Attachment.Filename":
			return v.Attachment.Filename
		}
	case baseBoost:
		switch f.path {
		case "BoosterTitle":
			return b.BoosterTitle
		case "Booster":
			return b.Booster
		case "Content":
			return b.Content
		}
	case baseReaction:
		switch f.path {
		case "Character":
			return rc.Character
		case "Title":
			return rc.Title
		}
	}
	panic("fastrender: unhandled string field " + fieldName(f))
}

func intField(f fieldRef, v *messageView, b *database.Boost) int64 {
	switch f.base {
	case baseMessage:
		switch f.path {
		case "CreatorID":
			return v.CreatorID
		case "ID":
			return v.ID
		case "RoomID":
			return v.RoomID
		}
	case baseBoost:
		switch f.path {
		case "ID":
			return b.ID
		case "MessageID":
			return b.MessageID
		case "BoosterID":
			return b.BoosterID
		}
	}
	panic("fastrender: unhandled int field " + fieldName(f))
}

func htmlField(f fieldRef, v *messageView) template.HTML {
	if f == (fieldRef{baseMessage, "HTML"}) {
		return v.HTML
	}
	panic("fastrender: unhandled html field " + fieldName(f))
}

func fieldName(f fieldRef) string {
	bases := [...]string{"message", "boost", "reaction"}
	if int(f.base) < len(bases) {
		return bases[f.base] + "." + f.path
	}
	return fmt.Sprintf("base%v.%s", f.base, f.path)
}

// ---- compiler ----

type fragmentCompiler struct {
	t      *template.Template
	avatar func(int64, time.Time) string
	progs  map[string]*fragmentProgram
	busy   map[string]bool
}

type fragmentProgram struct {
	base baseKind
	ops  []renderOp
}

// compileMessageRenderer parses a private copy of the templates, runs
// html/template's lazy escape pass on it (escaping fires on first execute)
// and compiles the message fragment from the escaped trees. The private
// copy keeps the server's own template set pristine until its first real
// execution, so Clone() and other pre-execution operations on it keep
// working. Any unsupported construct or a failed escape is an error; the
// caller falls back to the html/template path rather than serving
// miscompiled markup.
func compileMessageRenderer(secrets *rails.Secrets, avatars *avatarCache) (*messageRenderer, error) {
	fm := templateFuncs(secrets, avatars)
	compileTemplates, err := template.New("pages").Funcs(fm).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if err := escapeMessageFragments(compileTemplates); err != nil {
		return nil, err
	}
	// Render the fixed reaction bodies through the escaped set (the
	// reactions table is shared with the html/template path, so the
	// opReactionBody bytes equal that path's output).
	if err := prepareReactionBodies(compileTemplates); err != nil {
		return nil, err
	}
	// The renderer binds the same avatar helper as the template funcs, but
	// non-variadic: calling a variadic function through a func value
	// materializes the argument slice on the heap per call, which would be
	// the renderer's only allocation.
	avatar := func(id int64, updated time.Time) string { return avatarURL(avatars, secrets, id, updated) }
	c := &fragmentCompiler{t: compileTemplates, avatar: avatar, progs: map[string]*fragmentProgram{}, busy: map[string]bool{}}
	root, err := c.compileTemplate("message-uncached", baseMessage)
	if err != nil {
		return nil, err
	}
	return &messageRenderer{root: root.ops, avatar: avatar}, nil
}

func (c *fragmentCompiler) compileTemplate(name string, base baseKind) (*fragmentProgram, error) {
	if p, ok := c.progs[name]; ok {
		if p.base != base {
			return nil, fmt.Errorf("fastrender: %q compiled for %s but called at %s", name, fieldName(fieldRef{p.base, ""}), fieldName(fieldRef{base, ""}))
		}
		return p, nil
	}
	if c.busy[name] {
		return nil, fmt.Errorf("fastrender: template cycle through %q", name)
	}
	if tm := c.t.Lookup(name); tm == nil || tm.Tree == nil || tm.Tree.Root == nil {
		return nil, fmt.Errorf("fastrender: template %q has no tree", name)
	}
	c.busy[name] = true
	defer delete(c.busy, name)
	p := &fragmentProgram{base: base}
	c.progs[name] = p
	ops, err := c.compileList(c.t.Lookup(name).Tree.Root, base, map[string]baseKind{})
	if err != nil {
		delete(c.progs, name)
		return nil, fmt.Errorf("fastrender: template %q: %w", name, err)
	}
	p.ops = ops
	return p, nil
}

func cloneVars(vars map[string]baseKind) map[string]baseKind {
	out := make(map[string]baseKind, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

func (c *fragmentCompiler) compileList(list *parse.ListNode, base baseKind, vars map[string]baseKind) ([]renderOp, error) {
	var ops []renderOp
	for _, n := range list.Nodes {
		switch t := n.(type) {
		case *parse.TextNode:
			ops = append(ops, renderOp{kind: opText, text: string(t.Text)})
		case *parse.ActionNode:
			if len(t.Pipe.Decl) > 0 {
				// {{$m := .}}: declares a variable, produces no output.
				if len(t.Pipe.Cmds) != 1 || len(t.Pipe.Cmds[0].Args) != 1 {
					return nil, fmt.Errorf("unsupported variable declaration")
				}
				if _, ok := t.Pipe.Cmds[0].Args[0].(*parse.DotNode); !ok {
					return nil, fmt.Errorf("unsupported variable declaration with non-dot value")
				}
				for _, v := range t.Pipe.Decl {
					vars[v.Ident[0]] = base
				}
				continue
			}
			op, err := c.actionOp(t.Pipe, base, vars)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		case *parse.IfNode:
			cond, err := c.condOp(t.Pipe, base, vars)
			if err != nil {
				return nil, err
			}
			sub, err := c.compileList(t.List, base, cloneVars(vars))
			if err != nil {
				return nil, err
			}
			var other []renderOp
			if t.ElseList != nil {
				if other, err = c.compileList(t.ElseList, base, cloneVars(vars)); err != nil {
					return nil, err
				}
			}
			ops = append(ops, renderOp{kind: opCond, cond: cond, sub: sub, other: other})
		case *parse.RangeNode:
			kind, err := c.rangeOp(t.Pipe, base)
			if err != nil {
				return nil, err
			}
			itemBase := baseBoost
			if kind == opRangeReactions {
				itemBase = baseReaction
			}
			sub, err := c.compileList(t.List, itemBase, cloneVars(vars))
			if err != nil {
				return nil, err
			}
			var other []renderOp
			if t.ElseList != nil {
				if other, err = c.compileList(t.ElseList, base, cloneVars(vars)); err != nil {
					return nil, err
				}
			}
			ops = append(ops, renderOp{kind: kind, sub: sub, other: other})
		case *parse.TemplateNode:
			// {{template "name" .}}: the callee program is inlined with the
			// current dot as its base.
			if len(t.Pipe.Cmds) != 1 || len(t.Pipe.Cmds[0].Args) != 1 {
				return nil, fmt.Errorf("unsupported template call with pipeline arguments")
			}
			if _, ok := t.Pipe.Cmds[0].Args[0].(*parse.DotNode); !ok {
				return nil, fmt.Errorf("unsupported template call with non-dot pipeline")
			}
			sub, err := c.compileTemplate(t.Name, base)
			if err != nil {
				return nil, err
			}
			ops = append(ops, sub.ops...)
		case *parse.CommentNode:
			// no output
		default:
			return nil, fmt.Errorf("unsupported node %T", n)
		}
	}
	return ops, nil
}

// actionOp compiles {{...}} output actions from their escaped pipelines: the
// first command provides the value, the remaining commands are the escaping
// functions html/template chose for the context.
func (c *fragmentCompiler) actionOp(pipe *parse.PipeNode, base baseKind, vars map[string]baseKind) (renderOp, error) {
	if len(pipe.Cmds) == 0 {
		return renderOp{}, fmt.Errorf("empty pipeline")
	}
	esc := make([]string, 0, len(pipe.Cmds)-1)
	for _, cmd := range pipe.Cmds[1:] {
		if len(cmd.Args) != 1 {
			return renderOp{}, fmt.Errorf("unsupported pipeline command with arguments")
		}
		id, ok := cmd.Args[0].(*parse.IdentifierNode)
		if !ok {
			return renderOp{}, fmt.Errorf("unsupported non-escaper pipeline command")
		}
		if !knownEscaper(id.Ident) {
			return renderOp{}, fmt.Errorf("unsupported escaper %q", id.Ident)
		}
		esc = append(esc, id.Ident)
	}
	val, err := c.valueOp(pipe.Cmds[0], base, vars)
	if err != nil {
		return renderOp{}, err
	}
	return val.escape(esc)
}

func knownEscaper(name string) bool {
	switch name {
	case "_html_template_attrescaper", "_html_template_htmlescaper",
		"_html_template_urlescaper", "_html_template_urlnormalizer",
		"_html_template_urlfilter":
		return true
	}
	return false
}

// valueRef is a compile-time description of a pipeline value.
type valueRef struct {
	field fieldRef // field values
	fn    fnKind   // function values
	lit   string   // compile-time constant (asset URLs)
	num   bool     // field value is an int64 (digits escape to themselves)
	soft  bool     // literal render bare (no source field)
}

type fnKind uint8

const (
	fnNone fnKind = iota
	fnEpochA
	fnEpochU
	fnISO
	fnAvatar
	fnReactionBody
)

func (v valueRef) escape(esc []string) (renderOp, error) {
	chain := escName(esc)
	if v.fn == fnNone && v.field.base == baseMessage && v.field.path == "HTML" {
		if chain != "text" {
			return renderOp{}, fmt.Errorf("html field with escaping %v", esc)
		}
		return renderOp{kind: opHTMLRaw, f: v.field}, nil
	}
	if v.fn == fnNone && v.soft {
		// Constant (asset path) resolved at compile time.
		if chain != "url" {
			return renderOp{}, fmt.Errorf("constant with escaping %v", esc)
		}
		out := make([]byte, 0, len(v.lit))
		if !isSafeURL(v.lit) {
			out = append(out, filterFailsafe...)
		} else {
			out = appendURLAttr(out, v.lit)
		}
		return renderOp{kind: opText, text: string(out)}, nil
	}
	if v.fn == fnNone {
		switch chain {
		case "attr", "text", "url", "urlpre":
			if v.num {
				// Digits survive every escaper in these chains unchanged.
				return renderOp{kind: opInt, f: v.field}, nil
			}
			kind := opStrAttr
			switch chain {
			case "text":
				kind = opStrText
			case "url":
				kind = opStrURL
			case "urlpre":
				kind = opStrURLPre
			}
			return renderOp{kind: kind, f: v.field}, nil
		}
		return renderOp{}, fmt.Errorf("unsupported escaping %v for field", esc)
	}
	switch v.fn {
	case fnEpochA, fnEpochU:
		if chain != "attr" {
			return renderOp{}, fmt.Errorf("epoch with escaping %v", esc)
		}
		if v.fn == fnEpochA {
			return renderOp{kind: opEpochA}, nil
		}
		return renderOp{kind: opEpochU}, nil
	case fnISO:
		if chain != "attr" {
			return renderOp{}, fmt.Errorf("iso with escaping %v", esc)
		}
		return renderOp{kind: opISO}, nil
	case fnAvatar:
		if chain != "url" {
			return renderOp{}, fmt.Errorf("avatar with escaping %v", esc)
		}
		if v.field.base == baseBoost {
			return renderOp{kind: opAvatarBoost}, nil
		}
		return renderOp{kind: opAvatarMsg}, nil
	case fnReactionBody:
		// {{.}} over a reaction element: the pre-rendered body, printed raw
		// (html/template applies htmlescaper to template.HTML, which is the
		// identity).
		if chain != "text" {
			return renderOp{}, fmt.Errorf("reaction body with escaping %v", esc)
		}
		return renderOp{kind: opReactionBody}, nil
	}
	return renderOp{}, fmt.Errorf("unknown function value")
}

// escName classifies the escaping pipeline html/template attached to a
// value. "" means an unsupported combination.
func escName(esc []string) string {
	url := "_html_template_urlfilter"
	norm := "_html_template_urlnormalizer"
	attr := "_html_template_attrescaper"
	htmlEsc := "_html_template_htmlescaper"
	switch {
	case len(esc) == 1 && esc[0] == attr:
		return "attr"
	case len(esc) == 1 && esc[0] == htmlEsc:
		return "text"
	case len(esc) == 2 && esc[0] == norm && esc[1] == attr:
		return "urlpre"
	case len(esc) == 3 && esc[0] == url && esc[1] == norm && esc[2] == attr:
		return "url"
	}
	return ""
}

// valueOp compiles the value command of an action or predicate.
func (c *fragmentCompiler) valueOp(cmd *parse.CommandNode, base baseKind, vars map[string]baseKind) (valueRef, error) {
	if len(cmd.Args) == 0 {
		return valueRef{}, fmt.Errorf("empty command")
	}
	switch a := cmd.Args[0].(type) {
	case *parse.FieldNode:
		return refForField(fieldRef{base, strings.Join(a.Ident, ".")}), nil
	case *parse.VariableNode:
		owner, ok := vars[a.Ident[0]]
		if !ok {
			return valueRef{}, fmt.Errorf("undeclared variable %s", a.Ident[0])
		}
		return refForField(fieldRef{owner, strings.Join(a.Ident[1:], ".")}), nil
	case *parse.ChainNode:
		v, err := c.chainValue(a, base, vars)
		if err != nil {
			return valueRef{}, err
		}
		v.num = intFieldRef(v.field)
		return v, nil
	case *parse.IdentifierNode:
		return c.funcValue(cmd, base, vars)
	case *parse.DotNode:
		// {{.}} — message-actions' quick-boosts range prints the current
		// reaction's pre-rendered form body (upstream PR #9 hoisted the
		// fields into reaction-body; the bodies are compiled constants).
		if base != baseReaction {
			return valueRef{}, fmt.Errorf("unsupported dot value at %s", fieldName(fieldRef{base, ""}))
		}
		return valueRef{fn: fnReactionBody}, nil
	case *parse.StringNode, *parse.BoolNode, *parse.NumberNode, *parse.NilNode:
		return valueRef{}, fmt.Errorf("unsupported constant interpolation %T", a)
	default:
		return valueRef{}, fmt.Errorf("unsupported value %T", a)
	}
}

// refForField tags a field reference with its compile-time Go type.
func refForField(f fieldRef) valueRef {
	return valueRef{field: f, num: intFieldRef(f)}
}

// intFieldRef reports whether the referenced field is an int64.
func intFieldRef(f fieldRef) bool {
	switch f.base {
	case baseMessage:
		switch f.path {
		case "CreatorID", "ID", "RoomID":
			return true
		}
	case baseBoost:
		switch f.path {
		case "ID", "MessageID", "BoosterID":
			return true
		}
	}
	return false
}

func (c *fragmentCompiler) chainValue(n *parse.ChainNode, base baseKind, vars map[string]baseKind) (valueRef, error) {
	owner := base
	var path []string
	switch inner := n.Node.(type) {
	case *parse.DotNode:
	case *parse.FieldNode:
		path = inner.Ident
	case *parse.VariableNode:
		var ok bool
		if owner, ok = vars[inner.Ident[0]]; !ok {
			return valueRef{}, fmt.Errorf("undeclared variable %s", inner.Ident[0])
		}
		path = inner.Ident[1:]
	default:
		return valueRef{}, fmt.Errorf("unsupported chain base %T", n.Node)
	}
	path = append(path, n.Field...)
	return valueRef{field: fieldRef{owner, strings.Join(path, ".")}}, nil
}

func (c *fragmentCompiler) funcValue(cmd *parse.CommandNode, base baseKind, vars map[string]baseKind) (valueRef, error) {
	name := cmd.Args[0].(*parse.IdentifierNode).Ident
	arg := func(i int) (fieldRef, error) {
		if len(cmd.Args) <= i {
			return fieldRef{}, fmt.Errorf("func %s: missing argument %d", name, i)
		}
		return c.argField(cmd.Args[i], base, vars)
	}
	switch name {
	case "epoch", "iso":
		f, err := arg(1)
		if err != nil {
			return valueRef{}, err
		}
		if len(cmd.Args) != 2 || f.base != baseMessage {
			return valueRef{}, fmt.Errorf("unsupported %s call", name)
		}
		if name == "iso" {
			if f.path != "CreatedAt" {
				return valueRef{}, fmt.Errorf("unsupported iso argument %s", fieldName(f))
			}
			return valueRef{fn: fnISO}, nil
		}
		switch f.path {
		case "CreatedAt":
			return valueRef{fn: fnEpochA}, nil
		case "UpdatedAt":
			return valueRef{fn: fnEpochU}, nil
		}
		return valueRef{}, fmt.Errorf("unsupported epoch argument %s", fieldName(f))
	case "avatar":
		if len(cmd.Args) != 3 {
			return valueRef{}, fmt.Errorf("avatar with %d arguments", len(cmd.Args)-1)
		}
		id, err := arg(1)
		if err != nil {
			return valueRef{}, err
		}
		upd, err := arg(2)
		if err != nil {
			return valueRef{}, err
		}
		if id.base == baseMessage && id.path == "CreatorID" && upd == (fieldRef{baseMessage, "CreatorUpdatedAt"}) {
			return valueRef{fn: fnAvatar, field: id}, nil
		}
		if id.base == baseBoost && id.path == "BoosterID" && upd == (fieldRef{baseBoost, "BoosterUpdatedAt"}) {
			return valueRef{fn: fnAvatar, field: id}, nil
		}
		return valueRef{}, fmt.Errorf("unsupported avatar arguments (%s, %s)", fieldName(id), fieldName(upd))
	case "asset":
		if len(cmd.Args) != 2 {
			return valueRef{}, fmt.Errorf("asset with %d arguments", len(cmd.Args)-1)
		}
		st, ok := cmd.Args[1].(*parse.StringNode)
		if !ok {
			return valueRef{}, fmt.Errorf("unsupported asset argument %T", cmd.Args[1])
		}
		return valueRef{soft: true, lit: assets.Path(st.Text)}, nil
	default:
		return valueRef{}, fmt.Errorf("unsupported function %q", name)
	}
}

// argField resolves a function argument node to a field reference.
func (c *fragmentCompiler) argField(n parse.Node, base baseKind, vars map[string]baseKind) (fieldRef, error) {
	switch a := n.(type) {
	case *parse.FieldNode:
		return fieldRef{base, strings.Join(a.Ident, ".")}, nil
	case *parse.VariableNode:
		owner, ok := vars[a.Ident[0]]
		if !ok {
			return fieldRef{}, fmt.Errorf("undeclared variable %s", a.Ident[0])
		}
		return fieldRef{owner, strings.Join(a.Ident[1:], ".")}, nil
	case *parse.ChainNode:
		v, err := c.chainValue(a, base, vars)
		if err != nil {
			return fieldRef{}, err
		}
		return v.field, nil
	default:
		return fieldRef{}, fmt.Errorf("unsupported argument %T", n)
	}
}

// condOp compiles an {{if}} predicate.
func (c *fragmentCompiler) condOp(pipe *parse.PipeNode, base baseKind, vars map[string]baseKind) (condKind, error) {
	if len(pipe.Cmds) != 1 || len(pipe.Cmds[0].Args) == 0 {
		return 0, fmt.Errorf("unsupported if pipeline")
	}
	cmd := pipe.Cmds[0]
	switch a := cmd.Args[0].(type) {
	case *parse.FieldNode:
		if strings.Join(a.Ident, ".") == "AllEmoji" && base == baseMessage {
			return condAllEmoji, nil
		}
		if strings.Join(a.Ident, ".") == "Attachment" && base == baseMessage {
			return condAttachment, nil
		}
		return 0, fmt.Errorf("unsupported if field %s", strings.Join(a.Ident, "."))
	case *parse.IdentifierNode:
		if a.Ident != "allEmoji" {
			return 0, fmt.Errorf("unsupported if function %q", a.Ident)
		}
		if len(cmd.Args) != 2 {
			return 0, fmt.Errorf("allEmoji with %d arguments", len(cmd.Args)-1)
		}
		f, err := c.argField(cmd.Args[1], base, vars)
		if err != nil {
			return 0, err
		}
		if f != (fieldRef{baseBoost, "Content"}) {
			return 0, fmt.Errorf("unsupported allEmoji argument %s", fieldName(f))
		}
		return condContentAllEmoji, nil
	default:
		return 0, fmt.Errorf("unsupported if predicate %T", a)
	}
}

// rangeOp classifies a {{range}} source.
func (c *fragmentCompiler) rangeOp(pipe *parse.PipeNode, base baseKind) (opKind, error) {
	if len(pipe.Cmds) != 1 || len(pipe.Cmds[0].Args) != 1 {
		return 0, fmt.Errorf("unsupported range pipeline")
	}
	switch a := pipe.Cmds[0].Args[0].(type) {
	case *parse.IdentifierNode:
		if a.Ident == "reactions" {
			return opRangeReactions, nil
		}
		return 0, fmt.Errorf("unsupported range source %q", a.Ident)
	case *parse.FieldNode:
		if strings.Join(a.Ident, ".") == "Boosts" && base == baseMessage {
			return opRangeBoost, nil
		}
		return 0, fmt.Errorf("unsupported range field %s", strings.Join(a.Ident, "."))
	default:
		return 0, fmt.Errorf("unsupported range source %T", a)
	}
}
