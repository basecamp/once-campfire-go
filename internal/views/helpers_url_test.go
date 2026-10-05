package views

import (
	"encoding/json"
	"os"
	"testing"
)

func TestBuildsRailsQueryStrings(t *testing.T) {
	if got := RoomsDirectsWithUsers([]int64{5, 6}); got != "/rooms/directs?user_ids%5B%5D=5&user_ids%5B%5D=6" {
		t.Errorf("got %s", got)
	}
	if got := WithQuery("/x", ParamOne("z", "a b"), ParamOne("a", "1")); got != "/x?a=1&z=a+b" {
		t.Errorf("got %s", got)
	}
}

// Against what Ruby answers in the reference (vectors/ruby_core.json), as the reference's
// ruby_compat tests do.
func TestEscapesLikeRuby(t *testing.T) {
	if got := CGIEscape("a*~ b-._+é"); got != "a%2A~+b-._%2B%C3%A9" {
		t.Errorf("CGIEscape: %s", got)
	}
	if got := urlEncode("a*~ b-._+é"); got != "a%2A~%20b-._%2B%C3%A9" {
		t.Errorf("urlEncode: %s", got)
	}
	var vectors struct {
		Strings []struct {
			Input                 string `json:"input"`
			HTMLEscape            string `json:"html_escape"`
			CGIEscape             string `json:"cgi_escape"`
			URLEncode             string `json:"url_encode"`
			AddressableUnreserved string `json:"addressable_unreserved"`
		} `json:"strings"`
	}
	readVectors(t, &vectors)
	for _, c := range vectors.Strings {
		if got := Escape(c.Input); got != c.HTMLEscape {
			t.Errorf("Escape(%q) is %q, Ruby's %q", c.Input, got, c.HTMLEscape)
		}
		if got := CGIEscape(c.Input); got != c.CGIEscape {
			t.Errorf("CGIEscape(%q) is %q, Ruby's %q", c.Input, got, c.CGIEscape)
		}
		if got := urlEncode(c.Input); got != c.URLEncode || got != c.AddressableUnreserved {
			t.Errorf("urlEncode(%q) is %q, Ruby's %q and %q", c.Input, got, c.URLEncode, c.AddressableUnreserved)
		}
	}
}

func readVectors(t *testing.T, v any) {
	t.Helper()
	raw, err := os.ReadFile("../../reference/vectors/ruby_core.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}
