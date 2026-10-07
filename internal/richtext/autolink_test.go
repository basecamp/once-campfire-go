package richtext

import "testing"

func TestAutoLinkCandidatesAndRewrittenEmails(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{`<div>ordinary w words &amp; &#64; text</div>`, `<div>ordinary w words &amp; &#64; text</div>`},
		{`www. WwW. ://`, `www. WwW. ://`},
		{`wWW.Example.org`, `<a target="_blank" href="http://wWW.Example.org">wWW.Example.org</a>`},
		{`www.K.example`, `<a target="_blank" href="http://www.K.example">www.K.example</a>`},
		{`HTTP://example.org/x`, `<a target="_blank" href="HTTP://example.org/x">HTTP://example.org/x</a>`},
		{`a@example.org`, `<a target="_blank" href="mailto:a@example.org">a@example.org</a>`},
		{`http://example.org/&#64;a.example`, `<a target="_blank" href="http://example.org/@a.example">http://example.org/@a.example</a>`},
		{`<a href="https://example.org">a@example.org</a> b@example.org`, `<a href="https://example.org">a@example.org</a> <a target="_blank" href="mailto:b@example.org">b@example.org</a>`},
	} {
		got, err := autoLink(tc.text)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q (%v), want %q", tc.text, got, err, tc.want)
		}
	}
}
