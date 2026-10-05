package views

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// ActionView::Helpers::TagHelper: attribute rendering (tag_options) and element builders.
//
// Attrs keeps insertion order, since Rails renders attributes in hash order. Build one with
// NewAttrs and chain setters, mirroring the Ruby options hash:
// NewAttrs().Class("btn").Data("turbo_frame", "_top") is `class: "btn", data: { turbo_frame: "_top" }`.

// isBooleanAttribute is TagHelper::BOOLEAN_ATTRIBUTES: rendered as name="name" when true, omitted
// when false.
func isBooleanAttribute(name string) bool {
	switch name {
	case "allowfullscreen", "allowpaymentrequest", "async", "autofocus", "autoplay", "checked", "compact", "controls", "declare",
		"default", "defaultchecked", "defaultmuted", "defaultselected", "defer", "disabled", "enabled", "formnovalidate", "hidden",
		"indeterminate", "inert", "ismap", "itemscope", "loop", "multiple", "muted", "nohref", "nomodule", "noresize", "noshade",
		"novalidate", "nowrap", "open", "pauseonexit", "playsinline", "readonly", "required", "reversed", "scoped", "seamless",
		"selected", "sortable", "truespeed", "typemustmatch", "visible":
		return true
	}
	return false
}

// isVoidElement is true for HTML void elements, which the tag.* builder renders without a
// closing tag.
func isVoidElement(name string) bool {
	switch name {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "keygen", "link", "meta", "source", "track", "wbr":
		return true
	}
	return false
}

type valueKind uint8

const (
	// noValue is a nil option: it keeps its key's position but is never rendered.
	noValue valueKind = iota
	// textValue is a plain string, escaped on output.
	textValue
	// safeValue is an html_safe string: only `"` is replaced on output.
	safeValue
	trueValue
	falseValue
)

// attrValue is an option's value (the reference's Value).
type attrValue struct {
	text string
	kind valueKind
}

func textAttr(s string) attrValue { return attrValue{s, textValue} }

func boolAttr(flag bool) attrValue {
	if flag {
		return attrValue{kind: trueValue}
	}
	return attrValue{kind: falseValue}
}

// String is the value as Ruby's to_s prints it.
func (v attrValue) String() string {
	switch v.kind {
	case trueValue:
		return "true"
	case falseValue:
		return "false"
	}
	return v.text
}

// asText is the value as plain text, or nil: what `options.remove(key)` hands on as a string.
func (v attrValue) asText() attrValue {
	if v.kind == noValue {
		return v
	}
	return textAttr(v.String())
}

// toValue converts an option's Go value: string is text, HTML is html_safe, bool is a flag,
// numbers are text of their decimal form (Rust's to_string), and nil or a nil pointer is nil.
func toValue(value any) attrValue {
	switch v := value.(type) {
	case nil:
		return attrValue{}
	case string:
		return textAttr(v)
	case HTML:
		return attrValue{string(v), safeValue}
	case bool:
		return boolAttr(v)
	case int:
		return textAttr(strconv.Itoa(v))
	case int64:
		return textAttr(strconv.FormatInt(v, 10))
	case float64:
		return textAttr(displayFloat(v, 64))
	case *string:
		if v == nil {
			return attrValue{}
		}
		return textAttr(*v)
	}
	r := reflect.ValueOf(value)
	if r.Kind() == reflect.Pointer {
		if r.IsNil() {
			return attrValue{}
		}
		return toValue(r.Elem().Interface())
	}
	if s, ok := value.(fmt.Stringer); ok {
		return textAttr(s.String())
	}
	switch r.Kind() {
	case reflect.String:
		return textAttr(r.String())
	case reflect.Bool:
		return boolAttr(r.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return textAttr(strconv.FormatInt(r.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return textAttr(strconv.FormatUint(r.Uint(), 10))
	case reflect.Float32:
		return textAttr(displayFloat(r.Float(), 32))
	case reflect.Float64:
		return textAttr(displayFloat(r.Float(), 64))
	}
	panic(fmt.Sprintf("views: %T can't be an attribute value", value))
}

// displayFloat is a float as Rust's Display writes it: the shortest digits, never an exponent.
func displayFloat(f float64, bitSize int) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	return strconv.FormatFloat(f, 'f', -1, bitSize)
}

type attrEntry struct {
	name  string
	value attrValue
}

// Attrs is an ordered options hash. Setting a key again replaces its value in place (Ruby hash
// assignment); a nil value keeps the key's position (Ruby's `options["value"] = nil`) but is never
// rendered. A nil *Attrs is an empty hash.
//
// Helpers take ownership of the options they're given, as the reference moves them: they may add
// to or rearrange them, so an Attrs isn't reused after it has been passed to one.
type Attrs struct {
	entries []attrEntry
	inline  [4]attrEntry
}

// NewAttrs is the reference's attrs(): an empty options hash.
func NewAttrs() *Attrs {
	a := &Attrs{}
	a.entries = a.inline[:0]
	return a
}

// put sets name, replacing an earlier value in place.
func (a *Attrs) put(name string, value attrValue) *Attrs {
	if a == nil {
		a = NewAttrs()
	}
	for i := range a.entries {
		if a.entries[i].name == name {
			a.entries[i].value = value
			return a
		}
	}
	a.entries = append(a.entries, attrEntry{name, value})
	return a
}

// Attr sets name, replacing an earlier value in place. A nil value (nil, or a nil pointer) is nil.
func (a *Attrs) Attr(name string, value any) *Attrs { return a.put(name, toValue(value)) }

// AttrOpt is attr_opt: passing a possibly-nil option; nil keeps the key's position unrendered.
func (a *Attrs) AttrOpt(name string, value any) *Attrs { return a.put(name, toValue(value)) }

// Data is `data: { key => value }`: data-key, with the key dasherized.
func (a *Attrs) Data(key string, value any) *Attrs {
	return a.put(prefixed("data-", key), toValue(value))
}

// Aria is `aria: { key => value }`.
func (a *Attrs) Aria(key string, value any) *Attrs {
	return a.put(prefixed("aria-", key), toValue(value))
}

// AriaHidden is `aria: { hidden: "true" }`, the most common option in Campfire's views.
func (a *Attrs) AriaHidden() *Attrs { return a.put("aria-hidden", textAttr("true")) }

func (a *Attrs) Class(value any) *Attrs        { return a.Attr("class", value) }
func (a *Attrs) ID(value any) *Attrs           { return a.Attr("id", value) }
func (a *Attrs) Style(value any) *Attrs        { return a.Attr("style", value) }
func (a *Attrs) Title(value any) *Attrs        { return a.Attr("title", value) }
func (a *Attrs) Alt(value any) *Attrs          { return a.Attr("alt", value) }
func (a *Attrs) Role(value any) *Attrs         { return a.Attr("role", value) }
func (a *Attrs) Name(value any) *Attrs         { return a.Attr("name", value) }
func (a *Attrs) Type(value any) *Attrs         { return a.Attr("type", value) }
func (a *Attrs) Value(value any) *Attrs        { return a.Attr("value", value) }
func (a *Attrs) Target(value any) *Attrs       { return a.Attr("target", value) }
func (a *Attrs) Placeholder(value any) *Attrs  { return a.Attr("placeholder", value) }
func (a *Attrs) Autocomplete(value any) *Attrs { return a.Attr("autocomplete", value) }
func (a *Attrs) Accept(value any) *Attrs       { return a.Attr("accept", value) }
func (a *Attrs) Loading(value any) *Attrs      { return a.Attr("loading", value) }
func (a *Attrs) Tabindex(value any) *Attrs     { return a.Attr("tabindex", value) }
func (a *Attrs) Maxlength(value any) *Attrs    { return a.Attr("maxlength", value) }
func (a *Attrs) Rows(value any) *Attrs         { return a.Attr("rows", value) }

// Size is image_tag's `size:` option, expanded into width and height by ImageTag.
func (a *Attrs) Size(value any) *Attrs { return a.Attr("size", value) }

func (a *Attrs) Method(value any) *Attrs { return a.Attr("method", value) }

func (a *Attrs) Hidden() *Attrs             { return a.put("hidden", boolAttr(true)) }
func (a *Attrs) Required(value bool) *Attrs { return a.put("required", boolAttr(value)) }
func (a *Attrs) Autofocus() *Attrs          { return a.put("autofocus", boolAttr(true)) }
func (a *Attrs) Readonly() *Attrs           { return a.put("readonly", boolAttr(true)) }
func (a *Attrs) Disabled(value bool) *Attrs { return a.put("disabled", boolAttr(value)) }
func (a *Attrs) Checked(value bool) *Attrs  { return a.put("checked", boolAttr(value)) }

// Merge appends other's entries, overriding in place like Hash#merge!.
func (a *Attrs) Merge(other *Attrs) *Attrs {
	if other == nil {
		return a
	}
	for _, entry := range other.entries {
		a = a.put(entry.name, entry.value)
	}
	return a
}

// Get is the value of name as Ruby's to_s prints it, if it's set and not nil.
func (a *Attrs) Get(name string) (string, bool) {
	value := a.get(name)
	return value.String(), value.kind != noValue
}

// Has is true when name is a key, even with a nil value.
func (a *Attrs) Has(name string) bool {
	if a == nil {
		return false
	}
	for _, entry := range a.entries {
		if entry.name == name {
			return true
		}
	}
	return false
}

func (a *Attrs) Len() int {
	if a == nil {
		return 0
	}
	return len(a.entries)
}

// get is name's value; nil when absent.
func (a *Attrs) get(name string) attrValue {
	if a == nil {
		return attrValue{}
	}
	for _, entry := range a.entries {
		if entry.name == name {
			return entry.value
		}
	}
	return attrValue{}
}

// remove deletes name and returns its value (nil when absent).
func (a *Attrs) remove(name string) attrValue {
	if a == nil {
		return attrValue{}
	}
	for i, entry := range a.entries {
		if entry.name == name {
			a.entries = append(a.entries[:i], a.entries[i+1:]...)
			return entry.value
		}
	}
	return attrValue{}
}

// setDefault is `options[name] ||= value`: sets only when absent or nil.
func (a *Attrs) setDefault(name string, value attrValue) *Attrs {
	if a.get(name).kind == noValue {
		return a.put(name, value)
	}
	return a
}

// fetchOrSet is `options.fetch(name) { value }`: sets only when the key is absent (a nil stays nil).
func (a *Attrs) fetchOrSet(name string, value attrValue) *Attrs {
	if !a.Has(name) {
		return a.put(name, value)
	}
	return a
}

func (a *Attrs) clone() *Attrs {
	c := NewAttrs()
	if a != nil {
		c.entries = append(c.entries, a.entries...)
	}
	return c
}

// withDefaultData is `link_to(url, **attributes, data: defaults.merge(attributes.delete(:data)))`:
// the default data attributes go where the caller's first data-* attribute is (or at the end),
// ahead of the caller's own data attributes, which override them.
func (a *Attrs) withDefaultData(defaults []attrEntry) *Attrs {
	var entries []attrEntry
	if a != nil {
		entries = a.entries
	}
	position := len(entries)
	for i, entry := range entries {
		if strings.HasPrefix(entry.name, "data-") {
			position = i
			break
		}
	}
	before := NewAttrs()
	before.entries = append(before.entries, entries[:position]...)
	data := NewAttrs()
	data.entries = append(data.entries, defaults...)
	after := NewAttrs()
	for _, entry := range entries[position:] {
		if strings.HasPrefix(entry.name, "data-") {
			data.put(entry.name, entry.value)
		} else {
			after.put(entry.name, entry.value)
		}
	}
	return before.Merge(data).Merge(after)
}

// renderInto is tag_options: the attributes, each with a leading space.
func (a *Attrs) renderInto(b *strings.Builder) {
	if a == nil {
		return
	}
	for _, entry := range a.entries {
		renderAttr(b, entry.name, entry.value)
	}
}

// lenHint is about how many bytes renderInto writes, to size the tag's buffer.
func (a *Attrs) lenHint() int {
	if a == nil {
		return 0
	}
	n := 0
	for _, entry := range a.entries {
		n += len(entry.name) + len(entry.value.text) + 4
	}
	return n
}

// renderAttr is one attribute of tag_options, with Rails' value rules.
func renderAttr(b *strings.Builder, name string, value attrValue) {
	switch value.kind {
	case noValue:
	case trueValue, falseValue:
		if isBooleanAttribute(name) {
			if value.kind == trueValue {
				pushAttr(b, name, name)
			}
		} else {
			pushAttr(b, name, value.String())
		}
	case textValue:
		pushAttrStart(b, name)
		EscapeTo(b, value.text)
		b.WriteByte('"')
	case safeValue:
		pushAttrStart(b, name)
		html := value.text
		for {
			i := strings.IndexByte(html, '"')
			if i < 0 {
				break
			}
			b.WriteString(html[:i])
			b.WriteString("&quot;")
			html = html[i+1:]
		}
		b.WriteString(html)
		b.WriteByte('"')
	}
}

// pushAttrStart writes ` name="`.
func pushAttrStart(b *strings.Builder, name string) {
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
}

// pushAttr writes ` name="value"`, with value as is.
func pushAttr(b *strings.Builder, name, value string) {
	pushAttrStart(b, name)
	b.WriteString(value)
	b.WriteByte('"')
}

// Dasherize is String#dasherize.
func Dasherize(key string) string { return strings.ReplaceAll(key, "_", "-") }

// prefixed is prefix and the dasherized key: `data: { turbo_frame: ... }` is data-turbo-frame.
func prefixed(prefix, key string) string { return prefix + Dasherize(key) }

// tagLen is about how long a name tag with attrs and contentLen bytes of content is.
func tagLen(name string, attrs *Attrs, contentLen int) int {
	return 2*len(name) + attrs.lenHint() + contentLen + 8
}

// openTag writes `<name attributes` (the tag left open).
func openTag(b *strings.Builder, name string, attrs *Attrs) {
	b.WriteByte('<')
	b.WriteString(name)
	attrs.renderInto(b)
}

// openContentTag writes content_tag's opening tag. A textarea's content starts on a new line.
func openContentTag(b *strings.Builder, name string, attrs *Attrs) {
	openTag(b, name, attrs)
	b.WriteByte('>')
	if name == "textarea" {
		b.WriteByte('\n')
	}
}

func closeTag(b *strings.Builder, name string) {
	b.WriteString("</")
	b.WriteString(name)
	b.WriteByte('>')
}

// ContentTag is content_tag(name, content, options) with already-safe content.
func ContentTag(name string, attrs *Attrs, content HTML) HTML {
	var b strings.Builder
	b.Grow(tagLen(name, attrs, len(content)))
	openContentTag(&b, name, attrs)
	b.WriteString(string(content))
	closeTag(&b, name)
	return HTML(b.String())
}

// ContentTagText is content_tag with plain-text content, escaped.
func ContentTagText(name string, attrs *Attrs, content string) HTML {
	var b strings.Builder
	b.Grow(tagLen(name, attrs, len(content)))
	openContentTag(&b, name, attrs)
	EscapeTo(&b, content)
	closeTag(&b, name)
	return HTML(b.String())
}

// BuilderTag is tag.name(**options) from the tag builder: void elements have no closing tag and
// no slash, others render empty. Underscores in the name become dashes (tag.turbo_frame).
func BuilderTag(name string, attrs *Attrs) HTML {
	name = Dasherize(name)
	var b strings.Builder
	b.Grow(tagLen(name, attrs, 0))
	openTag(&b, name, attrs)
	b.WriteByte('>')
	if !isVoidElement(name) {
		closeTag(&b, name)
	}
	return HTML(b.String())
}

// LegacyTag is the legacy tag(:name, options), used by image_tag and form fields: always
// self-closing with " />".
func LegacyTag(name string, attrs *Attrs) HTML {
	var b strings.Builder
	b.Grow(tagLen(name, attrs, 0))
	openTag(&b, name, attrs)
	b.WriteString(" />")
	return HTML(b.String())
}
