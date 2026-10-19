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
	var (
		gzipRejected, identityRejected bool
		gzipQ, identityQ               = -1.0, -1.0
		gzipPref, identityPref         = 3, 3
		explicitGzip, explicitIdentity bool
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
	// One streaming pass over every token: there is no field cap, so a long
	// header (e.g. a wildcard followed by many br fields and a final
	// gzip;q=0) is parsed to its end, exactly as the reference does. The
	// first wildcard is remembered and expanded after the pass, once every
	// explicit token (anywhere in the header) has set the explicit flags the
	// expansion consults.
	wildcardSeen := false
	var wildcardQ float64
	wildcardPref := 3
	for header != "" {
		var part string
		if comma := strings.IndexByte(header, ','); comma >= 0 {
			part, header = header[:comma], header[comma+1:]
		} else {
			part, header = header, ""
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
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
		switch name {
		case "*":
			if !wildcardSeen {
				wildcardSeen = true
				wildcardQ, wildcardPref = q, pref
			}
		case "gzip":
			explicitGzip = true
			add("gzip", q, pref)
		case "identity":
			explicitIdentity = true
			add("identity", q, pref)
		}
	}
	if wildcardSeen {
		if !explicitGzip {
			add("gzip", wildcardQ, wildcardPref)
		}
		if !explicitIdentity {
			add("identity", wildcardQ, wildcardPref)
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
