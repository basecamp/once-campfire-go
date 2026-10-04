package richtext

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// The prefilter may only skip text that urlPattern cannot match.
func TestURLPrefilterKeepsEveryMatch(t *testing.T) {
	pieces := []string{"http", "HTTPS", "ftp", "Www", "wWw", "ww", "w", ".", "..", ":", "/", "//", "://", ":/", "a", "Z", "_", "0", " ", "\n", "<", ">", "&gt;", "é", "\u00a0", "mailto", "x"}
	rng := rand.New(rand.NewPCG(1, 2))
	inputs := []string{"", "www.", "www.a", "WWW.example", "http://", "xmpp://a", "a://b", "nowww.here", "ww.w", "w.ww.www.x"}
	for range 200_000 {
		var b strings.Builder
		for range rng.IntN(8) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		inputs = append(inputs, b.String())
	}
	for _, input := range inputs {
		if urlPattern.MatchString(input) && !mayContainURL(input) {
			t.Fatalf("%q matches but was skipped", input)
		}
	}
}

func BenchmarkDisplayMessage(b *testing.B) {
	for _, body := range []string{
		"<div>Hello there, the coffee machine is fixed! See you at 3pm.</div>",
		"<div>Read https://example.com/docs and www.example.org, then mail ops@example.com.</div>",
	} {
		b.Run(strings.Fields(body)[1], func(b *testing.B) {
			for b.Loop() {
				if _, err := Display(body, Context{Host: "example.com"}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
