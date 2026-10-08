package httpcompat

import (
	"strconv"
	"strings"
)

// Encoding selects the response content coding for an Accept-Encoding header:
// "gzip", "identity", or "" when the client accepts neither. It is the single
// implementation shared by front.Deflate, which negotiates the coding it
// applies itself, and the application, which must only send a pre-encoded body
// when this same selection is gzip — front.Deflate passes a pre-set
// Content-Encoding through untouched, so the two layers must agree exactly.
//
// The rules mirror the middleware's historical behaviour, including its
// treatment of duplicates: a q=0 occurrence rejects the token outright, a
// wildcard expands to the unmentioned names, and gzip wins ties against
// identity. The scan is allocation-free: tokens are parsed in place into a
// stack array and the winner is the highest-ranked non-rejected gzip or
// identity token (quality first, then preference), which is the ordering the
// middleware used to obtain by sorting the token list.
func Encoding(header string) string {
	var accepts [16]encodingItem
	count := 0
	for count < len(accepts) {
		var part string
		if comma := strings.IndexByte(header, ','); comma >= 0 {
			part, header = header[:comma], header[comma+1:]
		} else {
			part, header = header, ""
		}
		part = strings.TrimSpace(part)
		if part != "" {
			name, params, _ := strings.Cut(part, ";")
			name = strings.TrimSpace(name)
			q := 1.0
			params = strings.TrimSpace(params)
			if strings.HasPrefix(params, "q=") {
				q = quality(params[2:])
			}
			pref := 2
			if name == "gzip" {
				pref = 0
			} else if name == "identity" {
				pref = 1
			}
			accepts[count] = encodingItem{name: name, q: q, pref: pref}
			count++
		}
		if header == "" {
			break
		}
	}

	explicitGzip, explicitIdentity := false, false
	for i := 0; i < count; i++ {
		switch accepts[i].name {
		case "gzip":
			explicitGzip = true
		case "identity":
			explicitIdentity = true
		}
	}

	var (
		gzipRejected, identityRejected bool
		gzipQ, identityQ               = -1.0, -1.0
		gzipPref, identityPref         = 3, 3
		expandedIdentity               bool
	)
	add := func(name string, q float64, pref int) {
		if q == 0 {
			switch name {
			case "gzip":
				gzipRejected = true
			case "identity":
				identityRejected = true
			}
		}
		switch name {
		case "gzip":
			if q > gzipQ || (q == gzipQ && pref < gzipPref) {
				gzipQ, gzipPref = q, pref
			}
		case "identity":
			expandedIdentity = true
			if q > identityQ || (q == identityQ && pref < identityPref) {
				identityQ, identityPref = q, pref
			}
		}
	}
	wildcard := false
	for i := 0; i < count; i++ {
		item := accepts[i]
		if item.name != "*" {
			add(item.name, item.q, item.pref)
			continue
		}
		if wildcard {
			continue
		}
		wildcard = true
		if !explicitGzip {
			add("gzip", item.q, item.pref)
		}
		if !explicitIdentity {
			add("identity", item.q, item.pref)
		}
	}

	bestName := ""
	bestQ := -1.0
	bestPref := 3
	consider := func(name string, q float64, pref int, rejected bool) {
		if rejected || q < 0 {
			return
		}
		if q > bestQ || (q == bestQ && pref < bestPref) {
			bestName, bestQ, bestPref = name, q, pref
		}
	}
	consider("gzip", gzipQ, gzipPref, gzipRejected)
	consider("identity", identityQ, identityPref, identityRejected)
	if bestName != "" {
		return bestName
	}
	// The middleware appended an identity fallback whenever the header never
	// mentioned identity at all; that fallback is never rejected.
	if !expandedIdentity {
		return "identity"
	}
	return ""
}

// encodingItem is one parsed Accept-Encoding token.
type encodingItem struct {
	name string
	q    float64
	pref int
}

// quality parses the numeric prefix of a q= parameter. A malformed value
// yields 0, exactly as the original inline parse did: the failed ParseFloat
// result was assigned unconditionally.
func quality(value string) float64 {
	end := 0
	for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
		end++
	}
	if end == 0 {
		return 1.0
	}
	q, _ := strconv.ParseFloat(value[:end], 64)
	return q
}
