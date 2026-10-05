// Package httpcompat implements Rails HTTP contracts on net/http.
package httpcompat

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type MIME struct {
	Symbol, Type         string
	Synonyms, Extensions []string
}

// TypeOf is the MIME type registered for a format ("html" is "text/html"), or "".
func TypeOf(format string) string {
	for _, m := range mimeTypes {
		if m.Symbol == format {
			return m.Type
		}
	}
	return ""
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

var qSeparator = regexp.MustCompile(`;\s*q="?`)
var numberPrefix = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?`)

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
		if loc := qSeparator.FindStringIndex(value); loc != nil {
			value = value[:loc[0]]
		}
		if strings.TrimSpace(value) == "" {
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
		fields := qSeparator.Split(value, -1)
		for len(fields) > 0 && fields[len(fields)-1] == "" {
			fields = fields[:len(fields)-1]
		}
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			continue
		}
		names := expand(name)
		if names == nil {
			names = []string{name}
		}
		for _, name := range names {
			q := 1.0
			if len(fields) > 1 {
				q = rubyFloat(fields[1])
			} else if name == "*/*" {
				q = 0
			}
			items = append(items, item{name, math.Trunc(q * 100)})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].q > items[j].q })
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
