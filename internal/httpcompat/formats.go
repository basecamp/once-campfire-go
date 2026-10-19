// Package httpcompat implements Rails HTTP contracts on net/http.
package httpcompat

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type MIME struct {
	Symbol, Type         string
	Synonyms, Extensions []string
}

func extension(value string) string {
	for _, m := range mimeTypes {
		if m.Symbol == value || slices.Contains(m.Extensions, value) {
			return m.Symbol
		}
	}
	return ""
}

var mimeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9!#$&\-^_.+]{0,126}$`)

func lookup(value string) (string, error) {
	value = strings.TrimRight(strings.SplitN(value, ";", 2)[0], " \t\r\n\v\f")
	if value == "*/*" {
		return value, nil
	}
	for _, m := range mimeTypes {
		if m.Type == value || slices.Contains(m.Synonyms, value) {
			return m.Symbol, nil
		}
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !mimeName.MatchString(parts[0]) || parts[1] != "*" && !mimeName.MatchString(parts[1]) {
		return "", errors.New("invalid MIME type")
	}
	return "", nil
}
func expand(value string) []string {
	var prefix string
	for _, p := range []string{"text/*", "application/*"} {
		if strings.HasPrefix(value, p) {
			prefix = strings.TrimSuffix(p, "*")
		}
	}
	if prefix == "" {
		return nil
	}
	var out []string
	for _, m := range mimeTypes {
		matched := strings.Contains(m.Type, prefix)
		for _, syn := range m.Synonyms {
			matched = matched || strings.Contains(syn, prefix)
		}
		if matched {
			out = append(out, m.Type)
		}
	}
	return out
}

var numberPrefix = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?`)

// qParam returns the index range of the next q parameter at or after from —
// `;` followed by optional whitespace, `q`, `=` and the reference
// `;\s*q="?` match's optional quote — as (start of ';', end after the
// quote), or ok=false when there is none.
func qParam(item string, from int) (start, end int, ok bool) {
	space := func(c byte) bool {
		return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
	}
	for i := from; i < len(item); i++ {
		if item[i] != ';' {
			continue
		}
		j := i + 1
		for j < len(item) && space(item[j]) {
			j++
		}
		if j+1 >= len(item) || item[j] != 'q' || item[j+1] != '=' {
			continue
		}
		end := j + 2
		if end < len(item) && item[end] == '"' {
			end++
		}
		return i, end, true
	}
	return 0, 0, false
}

// cutQuality splits an accept item at its first q parameter, returning the
// media range and the first q value (cut at the item's next q parameter,
// i.e. the reference's fields[1]). found reports whether any q value is
// present after the reference's trailing-empty-field trim: an empty first
// value under a later non-empty field is present (rubyFloat turns it into
// 0), while an item whose q fields are all empty had them all trimmed and
// carries no q.
func cutQuality(item string) (name, q string, found bool) {
	firstStart, firstEnd, ok := qParam(item, 0)
	if !ok {
		return item, "", false
	}
	next, _, hasNext := qParam(item, firstEnd)
	if !hasNext {
		next = len(item)
	}
	q = item[firstEnd:next]
	// found: any non-empty field after the name (all-empty runs and a
	// trailing empty field are trimmed, exactly like the reference's loop).
	for cur := firstEnd; ; {
		start, end, ok := qParam(item, cur)
		if !ok {
			found = cur < len(item)
			break
		}
		if start > cur {
			found = true
			break
		}
		cur = end // empty field between adjacent parameters; keep walking
	}
	return item[:firstStart], q, found
}

func acceptItems(value string) []string {
	var items []string
	for i := 0; i < len(value); {
		if strings.ContainsRune(", \t\r\n\v\f\"", rune(value[i])) {
			i++
			continue
		}
		start := i
		i++
		for i < len(value) && value[i] != ',' {
			if value[i] == '"' {
				end := strings.IndexByte(value[i+1:], '"')
				if end < 0 {
					break
				}
				i += end + 2
			} else {
				i++
			}
		}
		items = append(items, value[start:i])
	}
	return items
}
func ParseAccept(value string) ([]string, error) {
	if !strings.Contains(value, ",") {
		// Strip a q parameter on presence, not on whether its value survives
		// empty-field trimming: "text/html;q=" must parse as html, the same
		// way the multi-item path below cuts every item at its q parameter.
		if start, _, ok := qParam(value, 0); ok {
			value = value[:start]
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil
		}
		if expanded := expand(value); expanded != nil {
			out := []string{}
			for _, v := range expanded {
				symbol, _ := lookup(v)
				out = append(out, symbol)
			}
			return out, nil
		}
		symbol, err := lookup(value)
		if symbol == "" {
			return nil, err
		}
		return []string{symbol}, err
	}
	type item struct {
		name string
		q    float64
	}
	var items []item
	for _, value := range acceptItems(value) {
		name, qfield, found := cutQuality(value)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names := expand(name)
		if names == nil {
			names = []string{name}
		}
		for _, name := range names {
			q := 1.0
			if found {
				q = rubyFloat(qfield)
			} else if name == "*/*" {
				q = 0
			}
			items = append(items, item{name, math.Trunc(q * 100)})
		}
	}
	// Stable sort by q descending; an insertion sort matches
	// sort.SliceStable's ordering for the few items an Accept header can
	// carry and avoids the reflect machinery.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].q < items[j].q; j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
	find := func(name string) int {
		for i, x := range items {
			if x.name == name {
				return i
			}
		}
		return -1
	}
	text, app := find("text/xml"), find("application/xml")
	if text >= 0 && app >= 0 {
		items[app].q = max(items[app].q, items[text].q)
		if app > text {
			items[app], items[text] = items[text], items[app]
			app, text = text, app
		}
		items = append(items[:text], items[text+1:]...)
	} else if text >= 0 {
		items[text].name = "application/xml"
	}
	if app >= 0 {
		q := items[app].q
		for i := app; i < len(items) && items[i].q >= q; i++ {
			if strings.HasSuffix(items[i].name, "+xml") {
				items[app], items[i] = items[i], items[app]
				app = i
			}
		}
	}
	out := []string{}
	for _, item := range items {
		symbol, err := lookup(item.name)
		if err != nil {
			return nil, err
		}
		if symbol != "" && !slices.Contains(out, symbol) {
			out = append(out, symbol)
		}
	}
	return out, nil
}

type FormatInput struct {
	Format                    *string
	Accept, ContentType, Path string
	XHR                       bool
}

func (i FormatInput) UsesAccept() bool {
	present := strings.TrimSpace(i.Accept) != ""
	compact := strings.Join(strings.Fields(i.Accept), "")
	browser := strings.Contains(compact, ",*/*") || strings.Contains(compact, "*/*,")
	return i.Format == nil && (i.XHR && (present || i.ContentType != "") || present && !browser)
}
func Formats(i FormatInput) ([]string, error) {
	if i.Format != nil {
		if format := extension(*i.Format); format != "" {
			return []string{format}, nil
		}
		return nil, nil
	}
	if i.UsesAccept() {
		value := strings.TrimSpace(i.Accept)
		if value != "" {
			return ParseAccept(value)
		}
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.SplitN(i.ContentType, ";", 2)[0], ",", 2)[0]))
		if ct == "" {
			return nil, nil
		}
		symbol, err := lookup(ct)
		if symbol == "" {
			return nil, err
		}
		return []string{symbol}, err
	}
	if dot := strings.LastIndexByte(i.Path, '.'); dot >= 0 {
		if format := extension(i.Path[dot+1:]); format != "" {
			return []string{format}, nil
		}
	}
	if i.XHR {
		return []string{"js"}, nil
	}
	return []string{"html"}, nil
}
func Negotiate(input FormatInput, available ...string) (string, error) {
	formats, err := Formats(input)
	if err != nil {
		return "", err
	}
	for _, f := range formats {
		if f == "*/*" && len(available) > 0 {
			return available[0], nil
		}
		if slices.Contains(available, f) {
			return f, nil
		}
	}
	if slices.Contains(available, "*/*") && len(formats) > 0 {
		return formats[0], nil
	}
	return "", nil
}

var numberUnderscore = regexp.MustCompile(`([0-9])_([0-9])`)
var signedHex = regexp.MustCompile(`^[+-]0[xX][0-9a-fA-F]+`)

func rubyFloat(value string) float64 {
	value = strings.TrimLeft(value, " \t\r\n\v\f")
	for numberUnderscore.MatchString(value) {
		value = numberUnderscore.ReplaceAllString(value, "${1}${2}")
	}
	if hex := signedHex.FindString(value); hex != "" {
		n, err := strconv.ParseUint(hex[3:], 16, 64)
		if err == nil {
			if hex[0] == '-' {
				return -float64(n)
			}
			return float64(n)
		}
	}
	n, _ := strconv.ParseFloat(numberPrefix.FindString(value), 64)
	return n
}
