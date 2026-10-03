// Package useragent preserves the useragent gem contracts used by Campfire.
package useragent

import (
	"regexp"
	"strconv"
	"strings"
)

type Value struct {
	Text          string
	Valid, Raised bool
}

func value(s string) Value { return Value{Text: s, Valid: true} }

var raised = Value{Raised: true}

type Product struct {
	Name, Version string
	Comment       []string
}
type Agent struct {
	Browser, Version, Platform, OS Value
	Bot, Mobile, MobileError       bool
	raw, kind                      string
	products                       []Product
}

func rubyStrip(s string) string { return strings.Trim(s, " \t\n\r\v\f\x00") }
func split(s, sep string) []string {
	parts := strings.Split(s, sep)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
func isSpace(b byte) bool { return strings.ContainsRune(" \t\n\r\v\f", rune(b)) }
func product(s string) (Product, int) {
	if s == "" {
		return Product{}, 0
	}
	start := 0
	for start < len(s) && (s[start] == '\'' || s[start] == '"') {
		start++
	}
	good := func(b byte) bool { return b != '/' && !isSpace(b) }
	if start == len(s) || !good(s[start]) {
		if start > 0 {
			start--
		} else {
			return Product{}, 0
		}
	}
	i := start + 1
	for i < len(s) && good(s[i]) {
		i++
	}
	p := Product{Name: s[start:i]}
	if i < len(s) && s[i] == '/' {
		i++
	}
	begin := i
	for i < len(s) && !isSpace(s[i]) && s[i] != ',' {
		i++
	}
	p.Version = s[begin:i]
	if i+1 < len(s) && isSpace(s[i]) && s[i+1] == '(' {
		if end := strings.IndexByte(s[i+2:], ')'); end >= 0 {
			p.Comment = split(s[i+2:i+2+end], "; ")
			if p.Comment == nil {
				p.Comment = []string{}
			}
			i += end + 3
		}
	} else if strings.HasPrefix(s[i:], ",gzip(gfe)") {
		i += 10
	}
	return p, i
}
func Parse(raw string) Agent {
	a := Agent{raw: raw, kind: "base"}
	rest := raw
	if rubyStrip(rest) == "" {
		rest = "Mozilla/4.0 (compatible)"
	}
	for {
		p, n := product(rest)
		if n == 0 {
			break
		}
		a.products = append(a.products, p)
		rest = rubyStrip(rest[n:])
	}
	first, last := a.first(), a.last()
	any := func(name string) bool {
		for _, p := range a.products {
			if p.Name == name {
				return true
			}
		}
		return false
	}
	comments := first.Comment
	joined := strings.Join(comments, " ")
	switch {
	case last.Name == "Edge":
		a.kind = "edge"
	case comments != nil && (strings.Contains(at(comments, 1).Text, "MSIE") || trident.MatchString(joined)):
		a.kind = "ie"
	case first.Name == "Opera" || last.Name == "OPR":
		a.kind = "opera"
	case a.containsProduct("micromessenger"):
		a.kind = "wechat"
	case any("Vivaldi"):
		a.kind = "vivaldi"
	case any("Chrome") || any("CriOS"):
		a.kind = "chrome"
	case any("iTunes"):
		a.kind = "itunes"
	case containsAny(at(comments, 0).Text, "PLAYSTATION 3", "PlayStation Vita", "PlayStation 4"):
		a.kind = "playstation"
	case len(a.products) >= 3 && a.products[0].Name == "Podcast" && a.products[1].Name == "Addict" && a.products[2].Name == "-":
		a.kind = "podcast"
	case a.webkit().Valid:
		a.kind = "webkit"
	case first.Name == "Mozilla":
		a.kind = "gecko"
	case (any("NSPlayer") || any("Windows-Media-Player") || any("WMFSDK")) && !containsExact(first.Version, "4.1.0.3856", "7.10.0.3059", "7.0.0.1956"):
		a.kind = "wmp"
	case any("AppleCoreMedia"):
		a.kind = "coremedia"
	case any("Lavf") || any("NSPlayer") && first.Version == "4.1.0.3856":
		a.kind = "lavf"
	}
	a.OS = a.operatingSystem()
	a.Platform = a.platform()
	a.Browser = a.browser()
	a.Version = a.version()
	app := a.application()
	a.Bot = app == nil || strings.Contains(app.Name, "bot") || a.detect("Chrome-Lighthouse") != nil
	for _, p := range a.products {
		for _, c := range p.Comment {
			a.Bot = a.Bot || strings.Contains(strings.ToLower(c), "bot")
		}
	}
	switch a.kind {
	case "opera":
		a.Mobile = a.operaMini()
	case "playstation":
		a.Mobile = a.Platform.Text == "PlayStation Vita"
	case "podcast":
		a.Mobile = true
	case "wmp":
		a.MobileError = a.OS.Raised
		a.Mobile = containsExact(a.OS.Text, "Windows Phone 8", "Windows Phone 8.1")
	default:
		a.Mobile = a.detect("Mobile") != nil
		for _, p := range a.products {
			for _, c := range p.Comment {
				a.Mobile = a.Mobile || c == "Mobile"
			}
		}
		if !a.Mobile {
			a.MobileError = a.OS.Raised
			a.Mobile = strings.Contains(a.OS.Text, "Android")
		}
		for _, c := range a.comments() {
			a.Mobile = a.Mobile || strings.HasPrefix(c, "IEMobile")
		}
	}
	return a
}
func containsExact(s string, options ...string) bool {
	for _, v := range options {
		if s == v {
			return true
		}
	}
	return false
}
func containsAny(s string, options ...string) bool {
	for _, v := range options {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
func at(v []string, i int) Value {
	if i < len(v) {
		return value(v[i])
	}
	return Value{}
}
func (a Agent) first() Product {
	if len(a.products) > 0 {
		return a.products[0]
	}
	return Product{}
}
func (a Agent) last() Product {
	if len(a.products) > 0 {
		return a.products[len(a.products)-1]
	}
	return Product{}
}
func (a Agent) detect(name string) *Product {
	for i := range a.products {
		if rubyLower(a.products[i].Name) == rubyLower(name) {
			return &a.products[i]
		}
	}
	return nil
}
func (a Agent) containsProduct(name string) bool {
	for _, p := range a.products {
		if strings.Contains(rubyLower(p.Name), name) {
			return true
		}
	}
	return false
}
func (a Agent) application() *Product {
	if containsExact(a.kind, "chrome", "vivaldi", "webkit", "itunes", "coremedia") {
		for i := range a.products {
			if len(a.products[i].Comment) > 0 {
				return &a.products[i]
			}
		}
		return nil
	}
	if len(a.products) > 0 {
		return &a.products[0]
	}
	return nil
}
func (a Agent) comments() []string {
	if p := a.application(); p != nil {
		return p.Comment
	}
	return nil
}
func (a Agent) baseVersion() Value {
	if p := a.application(); p != nil {
		return value(p.Version)
	}
	return Value{}
}
func (a Agent) webkit() Value {
	if p := a.detect("AppleWebKit"); p != nil {
		return value(p.Version)
	}
	for _, p := range a.products {
		for _, c := range p.Comment {
			if m := webkitComment.FindStringSubmatch(c); m != nil {
				return value(m[1])
			}
		}
	}
	return Value{}
}
func (a Agent) operaMini() bool {
	return strings.Contains(strings.Join(a.first().Comment, " "), "Opera Mini")
}
func (a Agent) browser() Value {
	switch a.kind {
	case "base":
		if p := a.application(); p != nil {
			return value(p.Name)
		}
		return Value{}
	case "chrome":
		if a.detect("Iron") != nil {
			return value("Iron")
		}
		return value("Chrome")
	case "playstation":
		c := at(a.comments(), 0).Text
		if strings.Contains(c, "PLAYSTATION 3") {
			return value("PS3 Internet Browser")
		}
		if a.last().Name == "Silk" {
			return value("Silk")
		}
		if strings.Contains(c, "PlayStation 4") {
			return value("PS4 Internet Browser")
		}
		return Value{}
	case "webkit":
		if strings.Contains(a.OS.Text, "Android") {
			return value("Android")
		}
		if a.Platform.Text == "BlackBerry" {
			return value("BlackBerry")
		}
		return value("Safari")
	case "gecko":
		for _, name := range []string{"PaleMoon", "Firefox", "Camino", "Iceweasel", "Seamonkey"} {
			if a.detect(name) != nil {
				return value(name)
			}
		}
		return value(a.first().Name)
	default:
		return value(map[string]string{"edge": "Edge", "ie": "Internet Explorer", "opera": "Opera", "wechat": "Wechat Browser", "vivaldi": "Vivaldi", "itunes": "iTunes", "podcast": "Podcast Addict", "wmp": "Windows Media Player", "coremedia": "AppleCoreMedia", "lavf": "libavformat"}[a.kind])
	}
}
func (a Agent) version() Value {
	detect := func(name string) Value {
		if p := a.detect(name); p != nil {
			return value(p.Version)
		}
		return raised
	}
	switch a.kind {
	case "base", "wmp", "coremedia":
		return a.baseVersion()
	case "edge", "vivaldi":
		return value(a.last().Version)
	case "ie":
		m := ieVersion.FindStringSubmatch(strings.Join(a.comments(), " "))
		if m != nil {
			return value(m[1])
		}
		return value("")
	case "opera":
		if a.operaMini() {
			for _, c := range a.comments() {
				if strings.Contains(c, "Opera Mini") {
					m := operaMiniVersion.FindStringSubmatch(c)
					if m != nil {
						return value(m[1])
					}
					break
				}
			}
			return value("")
		}
		if p := a.detect("Version"); p != nil {
			return value(p.Version)
		}
		if p := a.detect("OPR"); p != nil {
			return value(p.Version)
		}
		return a.baseVersion()
	case "wechat":
		return detect("MicroMessenger")
	case "chrome":
		if a.detect("CriOS") != nil {
			return detect("CriOS")
		}
		return detect("Chrome")
	case "itunes":
		return detect("iTunes")
	case "playstation":
		if !a.OS.Valid {
			return Value{}
		}
		if a.Browser.Text == "Silk" {
			return value(a.last().Version)
		}
		marker := a.Platform.Text + " "
		if a.Platform.Text == "PlayStation 3" {
			marker = "PLAYSTATION 3 "
		}
		parts := split(a.OS.Text, marker)
		if a.Platform.Valid && len(parts) > 0 {
			return value(parts[len(parts)-1])
		}
		return Value{}
	case "podcast":
		return Value{}
	case "webkit":
		if p := a.detect("Version"); p != nil {
			return value(p.Version)
		}
		if m := iosSafari.FindStringSubmatch(a.OS.Text); m != nil && a.Browser.Text == "Safari" {
			return value(m[1])
		}
		return value(webkitBuildVersions[a.webkit().Text])
	case "gecko":
		v := detect(a.Browser.Text)
		if !v.Raised && rubyStrip(v.Text) == "" {
			return a.baseVersion()
		}
		return v
	case "lavf":
		if a.detect("NSPlayer") != nil {
			return Value{}
		}
		return a.baseVersion()
	}
	return Value{}
}
func (a Agent) platform() Value {
	c := a.comments()
	first := at(c, 0)
	switch a.kind {
	case "base", "lavf":
		return Value{}
	case "edge", "ie", "wmp":
		return value("Windows")
	case "opera", "coremedia":
		if strings.Contains(first.Text, "Windows") {
			return value("Windows")
		}
		return first
	case "wechat":
		if strings.Contains(first.Text, "iPhone") {
			return value("iPhone")
		}
		for _, v := range c {
			if strings.Contains(v, "Android") {
				return value("Android")
			}
		}
		return first
	case "chrome", "vivaldi":
		if strings.Contains(first.Text, "Windows") {
			return value("Windows")
		}
		for _, needle := range []string{"CrOS", "Android"} {
			for _, v := range c {
				if strings.Contains(v, needle) {
					if needle == "CrOS" {
						return value("ChromeOS")
					}
					return value("Android")
				}
			}
		}
		return first
	case "webkit", "itunes":
		if strings.Contains(first.Text, "Windows") {
			return value("Windows")
		}
		if first.Text == "BB10" {
			return value("BlackBerry")
		}
		for _, v := range c {
			if strings.Contains(v, "Android") {
				return value("Android")
			}
		}
		return first
	case "playstation":
		for _, pair := range [][2]string{{"PLAYSTATION 3", "PlayStation 3"}, {"PlayStation 4", "PlayStation 4"}, {"PlayStation Vita", "PlayStation Vita"}} {
			if strings.Contains(a.OS.Text, pair[0]) {
				return value(pair[1])
			}
		}
		return Value{}
	case "podcast":
		if a.OS.Raised || !a.OS.Valid {
			return raised
		}
		if strings.Contains(a.OS.Text, "Android") {
			return value("Android")
		}
		return Value{}
	case "gecko":
		if containsExact(first.Text, "compatible", "Mobile") {
			return Value{}
		}
		if strings.HasPrefix(first.Text, "Windows ") {
			return value("Windows")
		}
		return first
	}
	return Value{}
}
func (a Agent) operatingSystem() Value {
	c := a.comments()
	first := at(c, 0)
	norm := func(v Value) Value {
		if v.Valid {
			v.Text = normalizeOS(v.Text)
		}
		return v
	}
	switch a.kind {
	case "base", "lavf":
		return Value{}
	case "edge":
		for _, p := range a.products {
			for _, c := range p.Comment {
				if m := windowsOS.FindString(c); m != "" {
					return value(normalizeOS(m))
				}
			}
		}
		return value("")
	case "ie":
		return value(normalizeOS(windowsOS.FindString(strings.Join(c, " "))))
	case "opera":
		if strings.Contains(first.Text, "Windows") {
			return norm(first)
		}
		return at(c, 1)
	case "chrome", "vivaldi", "wechat", "coremedia":
		if strings.Contains(first.Text, "Windows NT") {
			return norm(first)
		}
		if len(c) < 3 || strings.Contains(at(c, 1).Text, "Android") {
			return norm(at(c, 1))
		}
		return norm(at(c, 2))
	case "webkit", "itunes":
		if a.kind == "itunes" && strings.Contains(first.Text, "Windows") {
			full := at(c, 1).Text
			for _, name := range []string{"Windows 8.1", "Windows 8", "Windows 7", "Windows Vista", "Windows XP"} {
				if strings.Contains(full, name) {
					return value(name)
				}
			}
			return value("Windows")
		}
		if strings.Contains(first.Text, "Windows NT") {
			return norm(first)
		}
		if len(c) < 3 || strings.Contains(at(c, 1).Text, "Android") {
			return norm(at(c, 1))
		}
		for _, v := range c {
			if iosVersion.MatchString(v) {
				return value(normalizeOS(v))
			}
		}
		return norm(at(c, 2))
	case "playstation":
		if c != nil {
			return value(strings.Join(c, " "))
		}
		return Value{}
	case "podcast":
		if len(a.products) < 4 {
			return Value{}
		}
		p := a.products[3]
		if p.Name != "Dalvik" && p.Name != "Mozilla" {
			return Value{}
		}
		if p.Comment == nil {
			return raised
		}
		if len(p.Comment) > 3 {
			return at(p.Comment, 2)
		}
		if len(p.Comment) == 3 {
			return value("Android")
		}
		return Value{}
	case "gecko":
		if at(c, 1).Text == "U" {
			return norm(at(c, 2))
		}
		if strings.HasPrefix(first.Text, "Windows ") || strings.HasPrefix(first.Text, "Android") {
			return norm(first)
		}
		if first.Text == "Mobile" {
			return Value{}
		}
		return norm(at(c, 1))
	case "wmp":
		return windowsPlayerOS(a.baseVersion().Text)
	}
	return Value{}
}

var trident = regexp.MustCompile(`Trident.+rv:`)
var ieVersion = regexp.MustCompile(`(?:MSIE[ \t\n\r\v\f]|rv:)([0-9.]+)`)
var operaMiniVersion = regexp.MustCompile(`Opera Mini/([0-9.]+)`)
var webkitComment = regexp.MustCompile(`(?i)^AppleWebKit/([0-9.]+)`)
var macVersion = regexp.MustCompile(`(?:Intel|PPC) Mac OS X[ \t\r\n\v\f]*([0-9_.]+)?`)
var iosVersion = regexp.MustCompile(`CPU (?:iPhone |iPod )?OS ([0-9_]+) like Mac OS X`)
var iosSafari = regexp.MustCompile(`iOS ([0-9.]+)`)
var chromeOS = regexp.MustCompile(`CrOS[ \t\r\n\v\f][^ \t\r\n\v\f]+[ \t\r\n\v\f]([0-9]+(?:\.[0-9]+)*)`)
var windowsOS = regexp.MustCompile(`Windows NT [0-9.]+|Windows Phone (?:OS )?[0-9.]+`)

func normalizeOS(s string) string {
	if v, ok := windowsNames[s]; ok {
		return v
	}
	if m := macVersion.FindStringSubmatch(s); m != nil {
		if m[1] == "" {
			return "OS X"
		}
		return "OS X " + strings.ReplaceAll(m[1], "_", ".")
	}
	if m := iosVersion.FindStringSubmatch(s); m != nil {
		return "iOS " + strings.ReplaceAll(m[1], "_", ".")
	}
	if m := chromeOS.FindStringSubmatch(s); m != nil {
		return "ChromeOS " + m[1]
	}
	return s
}
func windowsPlayerOS(version string) Value {
	parts := VersionParts(version)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "i:") {
		return raised
	}
	part := func(i int) int {
		if i >= len(parts) {
			return -1
		}
		v, e := strconv.Atoi(strings.TrimPrefix(parts[i], "i:"))
		if e != nil {
			return -1
		}
		return v
	}
	major, build := part(0), part(3)
	os := "Windows"
	switch {
	case major >= 0 && major <= 4:
		os = map[int]string{3564: "Windows 98", 3925: "Windows 98", 3857: "Windows 9x", 3936: "Windows XP", 3938: "Windows 2000"}[build]
	case major == 7:
		if build == 3055 {
			os = "Windows 98"
		}
	case major == 8:
		os = "Windows XP"
	case major == 9 || major == 10:
		os = map[int]string{2980: "Windows 98/2000", 3268: "Windows 2000", 3367: "Windows 2000", 3270: "Windows 2000", 3802: "Windows XP", 4503: "Windows XP"}[build]
	case major == 11 || major == 12:
		os = map[int]string{9841: "Windows 10", 9858: "Windows 10", 9860: "Windows 10", 9879: "Windows 10", 9651: "Windows Phone 8.1", 9600: "Windows 8.1", 9200: "Windows 8", 7600: "Windows 7", 7601: "Windows 7", 6000: "Windows Vista", 6001: "Windows Vista", 6002: "Windows Vista", 5721: "Windows XP"}[part(2)]
	}
	if os == "" {
		os = "Windows"
	}
	return value(os)
}

func rubyLower(s string) string { return strings.ToLower(strings.ReplaceAll(s, "İ", "i\u0307")) }
