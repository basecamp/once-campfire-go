package views

import "testing"

func TestTurboHelpers(t *testing.T) {
	for _, c := range []struct{ got, want HTML }{
		{TurboPageRequiresReloadTag(), `<meta name="turbo-visit-control" content="reload">`},
		{TurboStreamFrom("s=="), `<turbo-cable-stream-source channel="Turbo::StreamsChannel" signed-stream-name="s=="></turbo-cable-stream-source>`},
		{TurboFrameTag("x", "f", NewAttrs().Attr("src", "/a?b&c").Class("c").Target("_top")), `<turbo-frame class="c" id="f" src="/a?b&amp;c" target="_top">x</turbo-frame>`},
		{TurboFrameTag("x", "f", nil), `<turbo-frame id="f">x</turbo-frame>`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
	if got := DomID("user", 5); got != "user_5" {
		t.Errorf("got %s", got)
	}
	if got := DomID("user", 5, "role"); got != "role_user_5" {
		t.Errorf("got %s", got)
	}
}
