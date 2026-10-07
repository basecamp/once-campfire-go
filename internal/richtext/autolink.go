package richtext

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// rails_autolink operates on serialized HTML, including entity references.
// Index tags once to avoid scanning the entire prefix for every link.
var urlPattern = regexp.MustCompile(`(?i)(?:(?:ed2k|ftp|http|https|irc|mailto|news|gopher|nntp|telnet|webcal|xmpp|callto|feed|svn|urn|aim|rsync|tag|ssh|sftp|rtsp|afs|file)://|www\.[a-z0-9_])[^ \t\r\n\v\f<\x{a0}"]+`)
var emailPattern = regexp.MustCompile("^[a-zA-Z0-9_.!#$%+-]\\.?[a-zA-Z0-9_.!#$%&'*/=?^`{|}~+-]*@[a-zA-Z0-9_-]+(?:\\.[a-zA-Z0-9_-]+)+")
var anchorPattern = regexp.MustCompile(`(?i)^<a\b.*?>`)

type tagIndex struct {
	lts, gts, closes []int
	anchors          [][2]int
	dangling         int
}

func indexTags(s string) tagIndex {
	t := tagIndex{dangling: -1}
	unclosed := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			t.lts = append(t.lts, i)
			if unclosed < 0 {
				unclosed = i
			}
			if i+4 <= len(s) && strings.EqualFold(s[i:i+4], "</a>") {
				t.closes = append(t.closes, i)
			}
			if len(t.anchors) == 0 || t.anchors[len(t.anchors)-1][1] <= i {
				if m := anchorPattern.FindStringIndex(s[i:]); m != nil {
					t.anchors = append(t.anchors, [2]int{i, i + m[1]})
				}
			}
		case '>':
			t.gts = append(t.gts, i)
			unclosed = -1
		case '\n':
			if t.dangling < 0 && unclosed >= 0 && unclosed+2 <= i {
				t.dangling = i
			}
		}
	}
	return t
}
func (t tagIndex) linked(start, end int) bool {
	open := t.dangling >= 0 && t.dangling < start
	lastGT := -1
	if n := sort.SearchInts(t.gts, start); n > 0 {
		lastGT = t.gts[n-1]
	}
	if n := sort.SearchInts(t.lts, lastGT+1); n < len(t.lts) && t.lts[n]+2 <= start {
		open = true
	}
	if open && len(t.gts) > 0 && t.gts[len(t.gts)-1] >= end {
		return true
	}
	n := sort.Search(len(t.anchors), func(i int) bool { return t.anchors[i][1] > start })
	if n == 0 {
		return false
	}
	a := t.anchors[n-1]
	c := sort.SearchInts(t.closes, a[1])
	return c == len(t.closes) || t.closes[c]+4 > start
}
func word(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsNumber(c) || unicode.IsMark(c) || unicode.Is(unicode.Pc, c) || c == '\u200c' || c == '\u200d'
}
func sanitizeString(s string) (string, error) {
	n, e := parse(s)
	if e != nil {
		return "", e
	}
	sanitizeDOM(n, "default")
	return serialize(n), nil
}

// maybeLinkable reports whether text contains any byte pattern the autoLink
// passes could match: a scheme followed by :// (the URL pattern's first
// branch), www. in either case (its second branch), or an @ (mandatory in
// the email pattern). The passes cannot match without one of these, so a
// negative scan lets autoLink return its input unchanged, which is exactly
// what the passes would produce (their no-match outputs are the input).
func maybeLinkable(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			if i+2 < len(s) && s[i+1] == '/' && s[i+2] == '/' {
				return true
			}
		case '@':
			return true
		case 'w', 'W':
			if i+3 < len(s) && (s[i+1] == 'w' || s[i+1] == 'W') && (s[i+2] == 'w' || s[i+2] == 'W') && s[i+3] == '.' {
				return true
			}
		}
	}
	return false
}

func autoLink(text string) (string, error) {
	if !maybeLinkable(text) {
		return text, nil
	}
	var out strings.Builder
	last := 0
	tags := indexTags(text)
	for _, m := range urlPattern.FindAllStringIndex(text, -1) {
		out.WriteString(text[last:m[0]])
		last = m[1]
		whole := text[m[0]:m[1]]
		if tags.linked(m[0], m[1]) {
			out.WriteString(whole)
			continue
		}
		href := []rune(whole)
		counts := map[rune]int{}
		for _, c := range href {
			counts[c]++
		}
		var punctuation []rune
		for len(href) > 0 {
			c := href[len(href)-1]
			if word(c) || strings.ContainsRune("/-=;", c) {
				break
			}
			href = href[:len(href)-1]
			punctuation = append(punctuation, c)
			counts[c]--
			opening := map[rune]rune{')': '(', ']': '[', '}': '{'}[c]
			if opening != 0 && counts[opening] > counts[c] {
				href = append(href, c)
				punctuation = punctuation[:len(punctuation)-1]
				break
			}
		}
		display := string(href)
		trailingGT := ""
		if strings.HasSuffix(display, "&gt;") {
			display = strings.TrimSuffix(display, "&gt;")
			trailingGT = "&gt;"
		}
		destination := display
		if strings.HasPrefix(strings.ToLower(destination), "www.") {
			destination = "http://" + destination
		}
		display, e := sanitizeString(display)
		if e != nil {
			return "", e
		}
		destination, e = sanitizeString(destination)
		if e != nil {
			return "", e
		}
		out.WriteString(`<a target="_blank" href="` + strings.ReplaceAll(destination, `"`, "&quot;") + `">` + display + `</a>`)
		for i := len(punctuation) - 1; i >= 0; i-- {
			out.WriteString(erbEscape(string(punctuation[i])))
		}
		out.WriteString(trailingGT)
	}
	out.WriteString(text[last:])
	return autoLinkEmails(out.String())
}
func emailLocal(c rune) bool {
	return c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.!#$%&'*/=?^`{|}~+-", c))
}
func autoLinkEmails(text string) (string, error) {
	if !strings.ContainsRune(text, '@') {
		return text, nil
	}
	var out strings.Builder
	copied, position := 0, 0
	tags := indexTags(text)
	for position < len(text) {
		var m []int
		previous, _ := utf8.DecodeLastRuneInString(text[:position])
		if position == 0 || !emailLocal(previous) {
			m = emailPattern.FindStringIndex(text[position:])
		}
		if m == nil {
			_, n := utf8.DecodeRuneInString(text[position:])
			position += n
			continue
		}
		start, end := position, position+m[1]
		email := text[start:end]
		out.WriteString(text[copied:start])
		if tags.linked(start, end) {
			out.WriteString(email)
		} else {
			sanitized, e := sanitizeString(email)
			if e != nil {
				return "", e
			}
			display := sanitized
			if sanitized == email {
				display = erbEscape(email)
			}
			href := "mailto:" + strings.ReplaceAll(url.QueryEscape(sanitized), "%40", "@")
			out.WriteString(`<a target="_blank" href="` + erbEscape(href) + `">` + display + `</a>`)
		}
		copied = end
		position = end
	}
	out.WriteString(text[copied:])
	return out.String(), nil
}
