package richtext

import (
	"strings"
	"testing"
)

func TestUntrustedMarkup(t *testing.T) {
	for _, input := range []string{`<script>alert(1)</script><b>safe</b>`, `<img src=x onerror=alert(1)><b>safe</b>`, `<svg><a onload=alert(1)>bad</a></svg><b>safe</b>`, `<a href="javascript:alert(1)">safe</a>`, `<a href="java&#x0a;script:alert(1)">safe</a>`, `<p style="background:url(javascript:x)" id="location" name="document">safe</p>`} {
		markup, plain := Render(input)
		if strings.Contains(markup, "alert") || strings.Contains(markup, "javascript") || strings.Contains(markup, "onload") || strings.Contains(markup, "name=") {
			t.Errorf("unsafe output: %s", markup)
		}
		if !strings.Contains(plain, "safe") {
			t.Errorf("lost text: %q", plain)
		}
	}
}
func TestEscaping(t *testing.T) {
	markup, plain := Render(`<p>&lt;script&gt; &amp; "hello"</p>`)
	if strings.Contains(markup, "<script>") || plain != `<script> & "hello"` {
		t.Fatalf("%q %q", markup, plain)
	}
}
