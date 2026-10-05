package views

import (
	"strings"
	"testing"
)

func TestTruncatesLikeRails(t *testing.T) {
	if got := Truncate("abcdef", 4, "…"); got != "abc…" {
		t.Errorf("got %s", got)
	}
	if got := Truncate("abcd", 4, "…"); got != "abcd" {
		t.Errorf("got %s", got)
	}
}

func TestBuildsSentences(t *testing.T) {
	for _, c := range []struct {
		items     []string
		connector string
		want      string
	}{
		{[]string{"A", "B"}, "+", "A+B"},
		{[]string{"A", "B", "C"}, "+", "A, B, and C"},
		{[]string{"A"}, " and ", "A"},
		{[]string{"A", "B"}, " and ", "A and B"},
	} {
		if got := ToSentence(c.items, c.connector); got != c.want {
			t.Errorf("ToSentence(%q, %q) = %q", c.items, c.connector, got)
		}
	}
}

func TestQrCodeLinksTakeTheURLURLSafeBase64Encoded(t *testing.T) {
	// Base64.urlsafe_encode64 in the reference: padded, with - and _.
	if got := LinkToZoomQrCode("", "http://x/?a"); !strings.Contains(string(got), `href="/qr_code/aHR0cDovL3gvP2E="`) {
		t.Errorf("got %s", got)
	}
	if got := LinkToZoomQrCode("", "http://x/?>?"); !strings.Contains(string(got), `href="/qr_code/aHR0cDovL3gvPz4_"`) {
		t.Errorf("got %s", got)
	}
}

func TestCapitalizesLikeRust(t *testing.T) {
	for in, want := range map[string]string{
		"chrome": "Chrome", "safari": "Safari", "": "", "éLODIE": "Élodie", "ß": "SS", "ﬁx": "FIx", "aİ": "Ai̇",
	} {
		if got := Capitalize(in); got != want {
			t.Errorf("Capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// The application helpers as the reference renders them (tests/golden).
func TestApplicationHelpersLikeRails(t *testing.T) {
	ctx := testContext()
	for _, c := range []struct{ got, want HTML }{
		{LinkBack(ctx), `<a class="btn" href="/"><img aria-hidden="true" src="/assets/arrow-left-abe40556.svg" width="20" height="20" /><span class="for-screen-reader">Go Back</span></a>`},
		{VersionBadge(ctx), `<span class="version-badge">parity</span>`},
		{ScriptAwareActionCableMetaTag(ctx), `<meta name="action-cable-url" content="/cable">`},
		{ButtonToCopyToClipboard("x", CurlTextLine("http://campfire.test/rooms/486777696/394959859-e0LbMoZhDhOs/messages")), `<button class="btn" data-controller="copy-to-clipboard" data-action="copy-to-clipboard#copy" data-copy-to-clipboard-success-class="btn--success" data-copy-to-clipboard-content-value="curl -d &#39;Hello!&#39; http://campfire.test/rooms/486777696/394959859-e0LbMoZhDhOs/messages">x</button>`},
		{ButtonToCopyToClipboard("x", CurlUploadLine("http://campfire.test/m")), `<button class="btn" data-controller="copy-to-clipboard" data-action="copy-to-clipboard#copy" data-copy-to-clipboard-success-class="btn--success" data-copy-to-clipboard-content-value="curl -F &quot;attachment=@/path/to/file&quot; http://campfire.test/m">x</button>`},
		{WebShareSessionButton("x", "http://campfire.test/session/transfers/t", "Your sign-in link", "Text."), `<button class="btn" hidden="hidden" data-controller="web-share" data-action="web-share#share" data-web-share-url-value="http://campfire.test/session/transfers/t" data-web-share-text-value="Text." data-web-share-title-value="Your sign-in link">x</button>`},
		{PageTitleTag(nil), `<title>Campfire</title>`},
		{MailTo("david@37signals.com"), `<a href="mailto:david@37signals.com">david@37signals.com</a>`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
	ctx.CurrentUser = &CurrentUser{ID: 127326141, Name: "David & Co", Administrator: true}
	if got := CurrentUserMetaTags(ctx); got != `<meta name="current-user-id" content="127326141" /><meta name="current-user-name" content="David &amp; Co" />` {
		t.Errorf("got %s", got)
	}
	empty := ""
	ctx.Account.HasLogo = true
	if got := BodyClasses(ctx, &empty); got != " admin account-has-logo" {
		t.Errorf("got %q", got)
	}
}
