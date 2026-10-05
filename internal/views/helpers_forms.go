package views

import "strings"

// form_with, its FormBuilder fields, button_to, hidden_field_tag and button_tag, reproducing
// ActionView's attribute order (form_helper.rb, form_tag_helper.rb, tags/*.rb and
// url_helper.rb#button_to in the reference image's Rails).
//
// A form is a block helper, so the <form> tag is built after its fields have rendered (that's how
// Rails learns about multipart from a file_field):
//
//	form := FormWith(RouteFirstRun()).Model("user").Class("center")
//	// the captured block renders form.TextField("name", nil, NewAttrs().Class("input")), ...
//	FormWithBlock(content, form)

// MethodTag is the hidden _method field (method_tag).
func MethodTag(method string) HTML {
	return LegacyTag("input", NewAttrs().Type("hidden").Name("_method").Value(method))
}

// Form is form_with(url:, model:, method:, id:, class:, data:) (the reference's FormWith).
type Form struct {
	action string
	method string
	// model:'s param key; fields are named after the method alone when it's empty.
	objectName string
	id         *string
	class      *string
	data       *Attrs
	// Shared with the forms fields_for makes, as any file_field among them makes the form multipart.
	multipart *bool
}

// FormWith is form_with(url:), posting.
func FormWith(url string) *Form {
	return &Form{action: url, method: "post", multipart: new(bool)}
}

// Model is model: — the param key fields are scoped under ("user", "account").
func (f *Form) Model(paramKey string) *Form {
	f.objectName = paramKey
	return f
}

// Method is method:; a persisted model: implies "patch", so pass it for those too.
func (f *Form) Method(method string) *Form {
	f.method = method
	return f
}

func (f *Form) ID(id string) *Form {
	f.id = &id
	return f
}

func (f *Form) Class(class string) *Form {
	f.class = &class
	return f
}

func (f *Form) Data(key string, value any) *Form {
	f.data = f.data.Data(key, value)
	return f
}

// AutoSubmit is auto_submit_form_with (FormsHelper): prepends the auto-submit Stimulus controller.
func (f *Form) AutoSubmit() *Form {
	existing, _ := f.data.Get("data-controller")
	f.data = f.data.put("data-controller", textAttr(strings.TrimSpace("auto-submit "+existing)))
	return f
}

// Multipart is multipart: true; also set by any FileField.
func (f *Form) Multipart() *Form {
	*f.multipart = true
	return f
}

// Open is `<form ...>` plus the _method hidden field (html_options_for_form_with +
// extra_tags_for_form). No authenticity_token: forgery protection is by Sec-Fetch-Site.
func (f *Form) Open() HTML {
	var b strings.Builder
	f.openInto(&b)
	return HTML(b.String())
}

func (f *Form) openInto(b *strings.Builder) {
	html := NewAttrs().AttrOpt("id", f.id).AttrOpt("class", f.class).Merge(f.data)
	if *f.multipart {
		html.put("enctype", textAttr("multipart/form-data"))
	}
	method := strings.ToLower(f.method)
	formMethod := "post"
	if method == "get" {
		formMethod = "get"
	}
	html.put("action", textAttr(f.action)).put("accept-charset", textAttr("UTF-8")).put("method", textAttr(formMethod))
	openTag(b, "form", html)
	b.WriteByte('>')
	switch method {
	case "get", "post", "":
	default:
		b.WriteString(string(MethodTag(method)))
	}
}

// Wrap is the whole form around already-rendered content (a block-less form_with passes "").
func (f *Form) Wrap(content HTML) HTML {
	var b strings.Builder
	b.Grow(len(content) + 256)
	f.openInto(&b)
	b.WriteString(string(content))
	b.WriteString("</form>")
	return HTML(b.String())
}

func (f *Form) TextField(method string, value *string, options *Attrs) HTML {
	return f.inputField("text", method, value, options)
}

func (f *Form) EmailField(method string, value *string, options *Attrs) HTML {
	return f.inputField("email", method, value, options)
}

func (f *Form) URLField(method string, value *string, options *Attrs) HTML {
	return f.inputField("url", method, value, options)
}

// PasswordField never renders the model's value (`{ value: nil }.merge!(options)`).
func (f *Form) PasswordField(method string, options *Attrs) HTML {
	return f.inputField("password", method, nil, NewAttrs().put("value", attrValue{}).Merge(options))
}

func (f *Form) HiddenField(method string, value *string, options *Attrs) HTML {
	return f.inputField("hidden", method, value, options)
}

func (f *Form) FileField(method string, options *Attrs) HTML {
	*f.multipart = true
	return f.inputField("file", method, nil, options)
}

// TextArea is Tags::TextArea#render: the value is the element's content, after a newline.
func (f *Form) TextArea(method string, value *string, options *Attrs) HTML {
	options = f.addDefaultNameAndID(method, options)
	var content string
	if v := options.remove("value"); v.kind != noValue {
		content = Escape(v.String())
	} else if value != nil {
		content = Escape(*value)
	}
	return ContentTag("textarea", options, HTML(content))
}

// CheckBox is form.check_box(method, options, checked_value, unchecked_value) (Tags::CheckBox#render):
// a hidden unchecked value, then the checkbox. current is the model's value, compared with
// checkedValue.
func (f *Form) CheckBox(method string, options *Attrs, checkedValue, uncheckedValue, current string) HTML {
	options = options.put("type", textAttr("checkbox")).put("value", textAttr(checkedValue))
	if current == checkedValue {
		options.put("checked", textAttr("checked"))
	}
	options = f.addDefaultNameAndID(method, options)

	hidden := NewAttrs()
	for _, key := range [...]string{"name", "disabled", "form"} {
		if options.Has(key) {
			hidden.put(key, options.get(key))
		}
	}
	hidden.Type("hidden").Value(uncheckedValue)
	return LegacyTag("input", hidden) + LegacyTag("input", options)
}

// FieldsFor is form.fields_for(:settings): a builder for object_name[settings].
func (f *Form) FieldsFor(name string) *Form {
	nested := *f
	nested.data = f.data.clone()
	nested.objectName = f.objectName + "[" + name + "]"
	return &nested
}

// tagName is Tags::Base#tag_name; a model-less form_with names fields after the method alone.
func (f *Form) tagName(method string) string {
	if f.objectName == "" {
		return method
	}
	return f.objectName + "[" + method + "]"
}

// tagID is Tags::Base#tag_id: the sanitized object name and method joined by "_".
func (f *Form) tagID(method string) string {
	if f.objectName == "" {
		return method
	}
	return sanitizeObjectName(f.objectName) + "_" + method
}

func (f *Form) addDefaultNameAndID(method string, options *Attrs) *Attrs {
	return options.fetchOrSet("name", textAttr(f.tagName(method))).fetchOrSet("id", textAttr(f.tagID(method)))
}

// inputField is Tags::TextField#render.
func (f *Form) inputField(fieldType, method string, value *string, options *Attrs) HTML {
	if fieldType == "file" {
		*f.multipart = true
	}
	if !options.Has("size") {
		options = options.put("size", options.get("maxlength"))
	}
	options = options.setDefault("type", textAttr(fieldType))
	if fieldType != "file" {
		options = options.fetchOrSet("value", toValue(value))
	}
	options = f.addDefaultNameAndID(method, options)
	return LegacyTag("input", options)
}

// sanitizeObjectName is `object_name.gsub(/\]\[|[^-a-zA-Z0-9:.]/, "_").delete_suffix("_")`.
func sanitizeObjectName(name string) string {
	var b strings.Builder
	for _, c := range strings.ReplaceAll(name, "][", "_") {
		if isASCIIAlphanumeric(c) || c == '-' || c == ':' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

func isASCIIAlphanumeric(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ButtonTag is form.button(options) { ... } / button_tag.
func ButtonTag(options *Attrs, content HTML) HTML {
	return ContentTag("button", buttonOptions(options), content)
}

// buttonOptions is button_tag's attributes: `{ name: "button", type: "submit" }` merged with the
// options.
func buttonOptions(options *Attrs) *Attrs {
	return NewAttrs().put("name", textAttr("button")).put("type", textAttr("submit")).Merge(options)
}

// HiddenFieldTag is hidden_field_tag(name, value, options).
func HiddenFieldTag(name string, value *string, options *Attrs) HTML {
	base := NewAttrs().Type("hidden").Name(name).ID(sanitizeToID(name)).AttrOpt("value", value)
	return LegacyTag("input", base.Merge(options))
}

// sanitizeToID is sanitize_to_id: "]" removed, other non-id characters become "_".
func sanitizeToID(name string) string {
	var b strings.Builder
	for _, c := range strings.ReplaceAll(name, "]", "") {
		if isASCIIAlphanumeric(c) || c == '-' || c == '_' || c == ':' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// buttonToFormLen is about how long button_to's form tag and _method field are.
const buttonToFormLen = 128

// ButtonTo is button_to(url, options) { content }. options may carry method ("delete", "put",
// "patch", "post" or "get"), form_class, and the button's own attributes.
func ButtonTo(url string, options *Attrs, content HTML) HTML {
	var b strings.Builder
	b.Grow(tagLen("button", options, len(content)+buttonToFormLen))
	method := "post"
	if v := options.remove("method"); v.kind != noValue {
		method = v.String()
	}
	formClass := "button_to"
	if v := options.remove("form_class"); v.kind != noValue {
		formClass = v.String()
	}
	formMethod := "post"
	if method == "get" {
		formMethod = "get"
	}
	form := NewAttrs().put("class", textAttr(formClass)).put("method", textAttr(formMethod)).put("action", textAttr(url))
	openTag(&b, "form", form)
	b.WriteByte('>')
	switch method {
	case "delete", "patch", "put":
		b.WriteString(string(MethodTag(method)))
	}
	openContentTag(&b, "button", options.put("type", textAttr("submit")))
	b.WriteString(string(content))
	closeTag(&b, "button")
	b.WriteString("</form>")
	return HTML(b.String())
}
