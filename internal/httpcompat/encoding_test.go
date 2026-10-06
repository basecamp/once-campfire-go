package httpcompat

import "testing"

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
	} {
		if got := Encoding(c.header); got != c.want {
			t.Errorf("Encoding(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}
