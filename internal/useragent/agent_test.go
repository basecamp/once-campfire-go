package useragent

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func scalar(v Value) any {
	if v.Raised {
		return map[string]any{"error": true}
	}
	if v.Valid {
		return v.Text
	}
	return nil
}
func check(t *testing.T, label string, want, got any) {
	t.Helper()
	if m, ok := want.(map[string]any); ok && m["error"] != nil {
		want = map[string]any{"error": true}
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s: want %#v got %#v", label, want, got)
	}
}
func TestUserAgentVectors(t *testing.T) {
	var data struct {
		Agents   []map[string]any `json:"user_agents"`
		Versions []struct {
			String string
			Nil    bool
			Parts  []string `json:"to_a"`
		}
		Comparisons []struct {
			A, B   string
			Cmp    int
			Lt, Eq bool
		}
	}
	raw, err := os.ReadFile("../../reference/vectors/campfire_user_agents.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	for _, c := range data.Agents {
		raw, _ := c["ua"].(string)
		a := Parse(raw)
		t.Run(raw, func(t *testing.T) {
			for name, v := range map[string]Value{"browser": a.Browser, "version": a.Version, "platform": a.Platform, "os": a.OS} {
				check(t, name, c[name], scalar(v))
			}
			check(t, "bot", c["bot"], a.Bot)
			var mobile any = a.Mobile
			if a.MobileError {
				mobile = map[string]any{"error": true}
			}
			check(t, "mobile", c["mobile"], mobile)
			p := a.View()
			fields := map[string]any{"ios": p.IOS, "android": p.Android, "mac": p.Mac, "windows": p.Windows, "chrome": p.Chrome, "firefox": p.Firefox, "safari": p.Safari, "edge": p.Edge, "mobile": p.Mobile, "desktop": p.Desktop, "apple_messages": p.AppleMessages, "browser": scalar(a.Browser), "operating_system": scalar(a.applicationOS())}
			if a.Browser.Raised || !a.Browser.Valid {
				for _, k := range []string{"chrome", "firefox", "safari", "edge"} {
					fields[k] = map[string]any{"error": true}
				}
			}
			if a.applicationOS().Raised {
				fields["windows"] = map[string]any{"error": true}
			}
			expected := c["application_platform"].(map[string]any)
			for k, v := range fields {
				check(t, "application "+k, expected[k], v)
			}
			blocked, raised := a.Blocked()
			var b any = blocked
			if raised {
				b = map[string]any{"error": true}
			}
			check(t, "blocked", c["blocked"], b)
		})
	}
	for _, v := range data.Versions {
		check(t, v.String+" parts", v.Parts, VersionParts(v.String))
		check(t, v.String+" nil", v.Nil, rubyStrip(v.String) == "")
	}
	for _, v := range data.Comparisons {
		if got := Compare(v.A, v.B); got != v.Cmp || (got < 0) != v.Lt || (v.A == v.B) != v.Eq {
			t.Errorf("compare %q %q: %d want %d", v.A, v.B, got, v.Cmp)
		}
	}
}
