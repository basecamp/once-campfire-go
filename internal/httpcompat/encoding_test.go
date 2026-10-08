package httpcompat

import (
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestEncodingSelection pins the negotiation shared by front.Deflate and the
// application's pre-encoded recorded responses. Duplicate tokens are the
// interesting edge: a q=0 occurrence rejects the name outright, so gzip and
// identity cannot disagree between the two layers.
func TestEncodingSelection(t *testing.T) {
	for _, c := range []struct{ header, want string }{
		{"", "identity"},
		{"gzip", "gzip"},
		{"identity", "identity"},
		{"gzip, identity", "gzip"},
		{"gzip, identity;q=0", "gzip"},
		{"gzip;q=0", "identity"},
		{"gzip;q=0.5", "gzip"},
		{"gzip;q=0.5, identity;q=1", "identity"},
		{"*;q=1", "gzip"},
		{"br", "identity"},
		{"gzip;q=0, identity;q=0", ""},
		{"gzip;q=0, gzip;q=1", "identity"},
		{"gzip, gzip;q=0", "identity"},
		{"gzip;q=0, gzip", "identity"},
		{"identity;q=0, identity", ""},
		{"GZIP", "identity"},
		{"gzip ; q=1", "gzip"},
		// Typical browser headers.
		{"gzip, deflate, br, zstd", "gzip"},
		{"gzip, deflate, br", "gzip"},
		{"deflate, gzip;q=1.0, *;q=0.5", "gzip"},
		{"deflate, identity;q=0.5", "identity"},
		{"zstd, br", "identity"},
		{"gzip;q=0.9, identity;q=0.1", "gzip"},
		{"*;q=0", ""},
		{"gzip;q=0, *;q=1", "identity"},
	} {
		if got := Encoding(c.header); got != c.want {
			t.Errorf("Encoding(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

// TestEncodingMatchesReference is the differential guard for the
// allocation-free rewrite: referenceEncoding is the original implementation
// (parse into a slice, expand wildcards, sort, return the first acceptable
// token), retained here as the oracle. Every generated header must select the
// same coding.
func TestEncodingMatchesReference(t *testing.T) {
	tokens := []string{
		"gzip", "identity", "*", "br", "zstd", "deflate",
		"gzip;q=0", "gzip;q=0.5", "gzip;q=1", "gzip;q=1.0", "gzip;q=2",
		"identity;q=0", "identity;q=0.5", "identity;q=1",
		"*;q=0", "*;q=0.5", "*;q=1",
		"gzip;q=.", "gzip;q=", "gzip;q=0;x=1", "GZIP", " gzip",
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		n := rng.Intn(5)
		var parts []string
		for j := 0; j < n; j++ {
			parts = append(parts, tokens[rng.Intn(len(tokens))])
		}
		header := strings.Join(parts, ", ")
		if got, want := Encoding(header), referenceEncoding(header); got != want {
			t.Fatalf("Encoding(%q) = %q, reference = %q", header, got, want)
		}
	}
}

// referenceEncoding is the pre-optimization implementation: it allocates the
// token slice, expansion, rejection map and sort, so it is kept only as a test
// oracle for Encoding.
func referenceEncoding(header string) string {
	type item struct {
		name       string
		q          float64
		preference int
	}
	var accepts []item
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, param, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		q := 1.0
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "q=") {
			value := strings.TrimPrefix(param, "q=")
			end := 0
			for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
				end++
			}
			if end > 0 {
				q, _ = strconv.ParseFloat(value[:end], 64)
			}
		}
		p := 2
		if name == "gzip" {
			p = 0
		} else if name == "identity" {
			p = 1
		}
		accepts = append(accepts, item{name, q, p})
		if len(accepts) == 16 {
			break
		}
	}
	var expanded []item
	wildcard := false
	for _, item := range accepts {
		if item.name != "*" {
			expanded = append(expanded, item)
			continue
		}
		if wildcard {
			continue
		}
		wildcard = true
		for _, name := range []string{"gzip", "identity"} {
			found := false
			for _, v := range accepts {
				found = found || v.name == name
			}
			if !found {
				copy := item
				copy.name = name
				expanded = append(expanded, copy)
			}
		}
	}
	rejected := map[string]bool{}
	hasIdentity := false
	for _, item := range expanded {
		if item.q == 0 {
			rejected[item.name] = true
		}
		hasIdentity = hasIdentity || item.name == "identity"
	}
	sort.SliceStable(expanded, func(i, j int) bool {
		if expanded[i].q == expanded[j].q {
			return expanded[i].preference < expanded[j].preference
		}
		return expanded[i].q > expanded[j].q
	})
	if !hasIdentity {
		expanded = append(expanded, item{name: "identity"})
	}
	for _, item := range expanded {
		if !rejected[item.name] && (item.name == "gzip" || item.name == "identity") {
			return item.name
		}
	}
	return ""
}
