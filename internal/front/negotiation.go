package front

import (
	"net/http"
	"strconv"
	"strings"
)

// ResponseEncoding shares private negotiation with completed-response caching.
func ResponseEncoding(header string) string { return encoding(header) }

// Private responses retain Rack's quality, tie-breaking and implicit-identity
// policy. Only the two available codings and first wildcard need state: scanning
// the complete field requires constant space and cannot miss a late exclusion.
func encoding(header string) string {
	type choice struct {
		present, rejected bool
		quality           float64
		preference        int
	}
	var gz, identity, wildcard choice
	accept := func(c *choice, quality float64, preference int) {
		if !c.present || quality > c.quality {
			c.quality = quality
		}
		c.present = true
		c.rejected = c.rejected || quality == 0
		c.preference = preference
	}
	for part := range strings.SplitSeq(header, ",") {
		name, parameters, _ := strings.Cut(strings.TrimSpace(part), ";")
		switch strings.TrimSpace(name) {
		case "gzip":
			accept(&gz, privateQuality(parameters), 0)
		case "identity":
			accept(&identity, privateQuality(parameters), 1)
		case "*":
			if !wildcard.present {
				accept(&wildcard, privateQuality(parameters), 2)
			}
		}
	}
	if !gz.present {
		gz = wildcard
	}
	if !identity.present {
		identity = wildcard
	}
	if gz.present && !gz.rejected && (!identity.present || identity.rejected ||
		gz.quality > identity.quality || gz.quality == identity.quality && gz.preference <= identity.preference) {
		return "gzip"
	}
	if !identity.present || !identity.rejected {
		return "identity"
	}
	return ""
}

func privateQuality(parameters string) float64 {
	value, ok := strings.CutPrefix(strings.TrimSpace(parameters), "q=")
	if !ok {
		return 1
	}
	end := 0
	for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
		end++
	}
	if end == 0 {
		return 1
	}
	quality, _ := strconv.ParseFloat(value[:end], 64)
	return quality
}

// Public streaming policy is deliberately different: explicit gzip/zstd only,
// the first declaration of each coding, zstd on equal quality, and no HEAD coding.
func publicEncoding(r *http.Request) string {
	if r.Method == "HEAD" {
		return ""
	}
	var gz, zs float64
	var hasGzip, hasZstd bool
	for part := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		name, parameters, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		if strings.EqualFold(name, "gzip") && !hasGzip {
			gz, hasGzip = publicQuality(parameters), true
		} else if strings.EqualFold(name, "zstd") && !hasZstd {
			zs, hasZstd = publicQuality(parameters), true
		}
		if hasGzip && hasZstd {
			break
		}
	}
	if zs > 0 && zs >= gz {
		return "zstd"
	}
	if gz > 0 {
		return "gzip"
	}
	return ""
}

func publicQuality(parameters string) float64 {
	quality := 1.0
	for parameter := range strings.SplitSeq(parameters, ";") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(parameter), "q="); ok {
			quality, _ = strconv.ParseFloat(value, 64)
			quality = min(1, max(0, quality))
		}
	}
	return quality
}
