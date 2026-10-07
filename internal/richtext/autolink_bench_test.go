package richtext

import (
	"regexp"
	"testing"
)

var benchURLPattern = regexp.MustCompile(`(?i)(?:(?:ed2k|ftp|http|https|irc|mailto|news|gopher|nntp|telnet|webcal|xmpp|callto|feed|svn|urn|aim|rsync|tag|ssh|sftp|rtsp|afs|file)://|www\.[a-z0-9_])[^ \t\r\n\v\f<\x{a0}"]+`)

const benchPresentation = `<div class="lexxy-content">
  <p>bench write 1</p>
</div>
`

func BenchmarkAutoLinkPlain(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := autoLink(benchPresentation); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUrlPatternNoMatch(b *testing.B) {
	for i := 0; i < b.N; i++ {
		benchURLPattern.FindAllStringIndex(benchPresentation, -1)
	}
}

func BenchmarkAutoLinkEmailsPlain(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := autoLinkEmails(benchPresentation); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAutoLinkWithURL(b *testing.B) {
	text := `<div class="lexxy-content">
  <p>see https://example.com/path?q=1 for details and also www.example.org</p>
</div>
`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := autoLink(text); err != nil {
			b.Fatal(err)
		}
	}
}
