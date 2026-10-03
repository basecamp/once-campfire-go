package httpcompat

import (
	"reflect"
	"testing"
)

func TestReferenceAcceptOrdering(t *testing.T) {
	for _, c := range []struct {
		accept string
		want   []string
	}{
		{"text/html;q=, application/json", []string{"html", "json"}},
		{"text/html; q=, application/json;q=0.5", []string{"html", "json"}},
		{"text/html;q=;q=, application/json;q=0.5", []string{"html", "json"}},
		{"text/html;q=0.5, application/json;q=", []string{"json", "html"}},
		{"text/html;q=;x=1, application/json", []string{"json", "html"}},
		{"text/html;q=;q=0.9, application/json;q=0.5", []string{"json", "html"}},
		{"text/html;q=0.4;q=0.9, application/json;q=0.5", []string{"json", "html"}},
		{"text/html;q=0.5.1, application/json;q=0.6", []string{"json", "html"}},
		{"text/html;q=0.5.1.2, application/json;q=0.49", []string{"html", "json"}},
		{"text/html;q=\"0.9\", application/json;q=0.5", []string{"html", "json"}},
		{"text/html;q=\"\"0.9, application/json;q=0.5", []string{"json", "html"}},
		{"text/html;q=1e-1, application/json;q=0.5", []string{"json", "html"}},
		{"text/html;q=1_0, application/json;q=5", []string{"html", "json"}},
		{"text/html;q=abc, application/json;q=0.1", []string{"json", "html"}},
		{"text/html;q=+0.3, application/json;q=0.2", []string{"html", "json"}},
		{"text/html;q= 0.3, application/json;q=0.2", []string{"html", "json"}},
		{"text/html;q=1e17, application/json;q=1e18", []string{"json", "html"}},
		{"text/html;q=1e19, application/json;q=1e20", []string{"json", "html"}},
		{"text/html;q=-0.001, application/json;q=0", []string{"html", "json"}},
		{"application/json;q=0, text/html;q=-0.001", []string{"json", "html"}},
		{"text/html;q=0.5, application/json;q=+0x1", []string{"json", "html"}},
		{"text/html;q=0.5, application/json;q=0x1", []string{"html", "json"}},
	} {
		got, err := ParseAccept(c.accept)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %v %v != %v", c.accept, got, err, c.want)
		}
	}
}
