package richtext

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
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

func indexTags(a *xhtml.Arena, s string) tagIndex {
	t := tagIndex{dangling: -1}
	if a != nil {
		// Preallocated slab regions sized by upper bounds (lts: every byte
		// could be '<'; gts likewise; closes and anchors are a fraction of
		// tags). Appends past these bounds reallocate from the arena slab
		// via spare-capacity growth; the common case allocates nothing.
		t.lts = a.IntsCap(len(s))
		t.gts = a.IntsCap(len(s))
		t.closes = a.IntsCap(len(s)/4 + 8)
		t.anchors = a.PairsCap(len(s)/6 + 8)
	}
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
			if (i+1 < len(s) && (s[i+1] == 'a' || s[i+1] == 'A')) && (len(t.anchors) == 0 || t.anchors[len(t.anchors)-1][1] <= i) {
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
func sanitizeString(a *xhtml.Arena, s string) (string, error) {
	n, e := parse(a, s)
	if e != nil {
		return "", e
	}
	sanitizeDOM(n, "default")
	return serialize(a, n), nil
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

func autoLink(a *xhtml.Arena, text string) (string, error) {
	if !maybeLinkable(text) {
		return text, nil
	}
	out := a.NewBuilder()
	last := 0
	tags := indexTags(a, text)
	for _, m := range urlPattern.FindAllStringIndex(text, -1) {
		m0, m1 := m[0], m[1]
		out.WriteString(text[last:m0])
		last = m1
		whole := text[m0:m1]
		if tags.linked(m0, m1) {
			out.WriteString(whole)
			continue
		}
		// Trim trailing non-word punctuation with bracket-balance
		// restoration, tracking occurrence counts as the reference's map
		// does. Only ASCII runes are ever compared (closing brackets are
		// ASCII), so a stack table serves; the reference's map entries for
		// other runes are never read.
		href := arenaRunes(a, whole)
		var counts [256]int32
		for _, c := range href {
			if c < 256 {
				counts[c]++
			}
		}
		punctuation := a.RunesCap(len(href))
		for len(href) > 0 {
			c := href[len(href)-1]
			if word(c) || strings.ContainsRune("/-=;", c) {
				break
			}
			href = href[:len(href)-1]
			punctuation = append(punctuation, c)
			if c < 256 {
				counts[c]--
			}
			opening := pair[c]
			if opening != 0 && int(counts[opening]) > int(counts[c]) {
				href = append(href, c)
				punctuation = punctuation[:len(punctuation)-1]
				break
			}
		}
		display := runesString(a, href)
		trailingGT := ""
		if strings.HasSuffix(display, "&gt;") {
			display = strings.TrimSuffix(display, "&gt;")
			trailingGT = "&gt;"
		}
		destination := display
		if strings.HasPrefix(strings.ToLower(destination), "www.") {
			destination = a.Concat("http://", destination)
		}
		display, e := sanitizeString(a, display)
		if e != nil {
			return "", e
		}
		destination, e = sanitizeString(a, destination)
		if e != nil {
			return "", e
		}
		out.WriteString(`<a target="_blank" href="`)
		out.WriteString(a.ReplaceAll(destination, `"`, "&quot;"))
		out.WriteString(`">`)
		out.WriteString(display)
		out.WriteString("</a>")
		for i := len(punctuation) - 1; i >= 0; i-- {
			out.WriteString(erbEscape(a, string(punctuation[i])))
		}
		out.WriteString(trailingGT)
	}
	out.WriteString(text[last:])
	return autoLinkEmails(a, out.String())
}

// pair maps a closing rune to its opening match — the reference builds this
// map literal on every link.
var pair = map[rune]rune{')': '(', ']': '[', '}': '{'}

// arenaRunes decodes s into a rune view in the arena's rune slab.
func arenaRunes(a *xhtml.Arena, s string) []rune {
	r := a.RunesCap(utf8.RuneCountInString(s))
	for _, c := range s {
		r = append(r, c)
	}
	return r
}

// runesString encodes the rune slice into the arena's byte slab.
func runesString(a *xhtml.Arena, r []rune) string {
	if len(r) == 0 {
		return ""
	}
	b := a.BytesCap(utf8.RuneLen(r[0]) * len(r))
	for _, c := range r {
		b = utf8.AppendRune(b, c)
	}
	return a.View(b)
}

func emailLocal(c rune) bool {
	return c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.!#$%&'*/=?^`{|}~+-", c))
}
func autoLinkEmails(a *xhtml.Arena, text string) (string, error) {
	if !strings.ContainsRune(text, '@') {
		return text, nil
	}
	out := a.NewBuilder()
	copied, position := 0, 0
	tags := indexTags(a, text)
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
			sanitized, e := sanitizeString(a, email)
			if e != nil {
				return "", e
			}
			display := sanitized
			if sanitized == email {
				display = erbEscape(a, email)
			}
			quoted := url.QueryEscape(sanitized)
			out.WriteString(`<a target="_blank" href="`)
			out.WriteString(erbEscape(a, a.ReplaceAll(a.Concat("mailto:", quoted), "%40", "@")))
			out.WriteString(`">`)
			out.WriteString(display)
			out.WriteString("</a>")
		}
		copied = end
		position = end
	}
	out.WriteString(text[copied:])
	return out.String(), nil
}
