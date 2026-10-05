package views

import "testing"

// testAssets are digested paths from the reference's golden pages (tests/golden/a/facts.json).
var testAssets = map[string]string{
	"arrow-left.svg":                   "/assets/arrow-left-abe40556.svg",
	"check.svg":                        "/assets/check-7897ff7e.svg",
	"globe.svg":                        "/assets/globe-8c54d23b.svg",
	"messages.svg":                     "/assets/messages-9395d503.svg",
	"notification-bell-everything.svg": "/assets/notification-bell-everything-cde41b14.svg",
}

func testContext() *ViewContext {
	return &ViewContext{
		AssetPath:  func(logical string) string { return testAssets[logical] },
		BaseURL:    "http://campfire.test",
		AppVersion: "parity",
		CableURL:   "/cable",
	}
}

func TestImageTag(t *testing.T) {
	ctx := testContext()
	for _, c := range []struct{ got, want HTML }{
		{ImageTag(ctx, "check.svg", NewAttrs().AriaHidden().Size(20)), `<img aria-hidden="true" src="/assets/check-7897ff7e.svg" width="20" height="20" />`},
		{ImageTag(ctx, "/x.png", NewAttrs().Size("20x30").Alt("a")), `<img alt="a" src="/x.png" width="20" height="30" />`},
		{ImageTag(ctx, "https://example.com/x.png", nil), `<img src="https://example.com/x.png" />`},
		{ImageTag(ctx, "messages.svg", nil), `<img src="/assets/messages-9395d503.svg" />`},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}
