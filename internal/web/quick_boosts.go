package web

import (
	"bytes"
	"errors"
	"html/template"
	"strconv"
	"strings"
)

// Every message renders the same eight quick-boost forms around its client ID and
// ID. Executing that range through html/template cost more than the rest of the
// message, so "quick-boosts" is rendered once with markers and split, and each
// message only fills in its values, escaped as html/template escapes them there.
type quickBoostForms struct {
	segments []string
	slots    []quickBoostSlot
}
type quickBoostSlot uint8

const (
	quickBoostClientID quickBoostSlot = iota + 1
	quickBoostID
)

// Values that pass through html/template's attribute and URL escaping unchanged.
const (
	quickBoostClientIDMarker = "campfireQuickBoostClientID"
	quickBoostIDMarker       = 7239104658113927
)

func compileQuickBoosts(t *template.Template) (*quickBoostForms, error) {
	var b bytes.Buffer
	data := struct {
		ClientID string
		ID       int64
	}{quickBoostClientIDMarker, quickBoostIDMarker}
	if err := t.ExecuteTemplate(&b, "quick-boosts", data); err != nil {
		return nil, err
	}
	html, id := b.String(), strconv.FormatInt(quickBoostIDMarker, 10)
	forms := &quickBoostForms{}
	for {
		c, i := strings.Index(html, quickBoostClientIDMarker), strings.Index(html, id)
		if c < 0 && i < 0 {
			forms.segments = append(forms.segments, html)
			break
		}
		if i < 0 || c >= 0 && c < i {
			forms.segments = append(forms.segments, html[:c])
			forms.slots = append(forms.slots, quickBoostClientID)
			html = html[c+len(quickBoostClientIDMarker):]
		} else {
			forms.segments = append(forms.segments, html[:i])
			forms.slots = append(forms.slots, quickBoostID)
			html = html[i+len(id):]
		}
	}
	if len(forms.slots) == 0 {
		return nil, errors.New("quick-boosts template has no message values")
	}
	return forms, nil
}

// html/template's escaping of a string in a quoted attribute value.
var attributeEscaper = strings.NewReplacer("\x00", "�", `"`, "&#34;", "&", "&amp;", "'", "&#39;", "+", "&#43;", "<", "&lt;", ">", "&gt;")

func (f *quickBoostForms) render(clientID string, id int64) template.HTML {
	escaped, number := attributeEscaper.Replace(clientID), strconv.FormatInt(id, 10)
	size := len(escaped)*len(f.slots) + len(number)*len(f.slots)
	for _, segment := range f.segments {
		size += len(segment)
	}
	var b strings.Builder
	b.Grow(size)
	for i, slot := range f.slots {
		b.WriteString(f.segments[i])
		if slot == quickBoostClientID {
			b.WriteString(escaped)
		} else {
			b.WriteString(number)
		}
	}
	b.WriteString(f.segments[len(f.segments)-1])
	return template.HTML(b.String())
}
