package web

import (
	"bytes"
	"math"
	"testing"
)

func TestQuickBoostsMatchTheTemplate(t *testing.T) {
	app, _, _, _ := testApp(t)
	forms, err := compileQuickBoosts(app.templates)
	if err != nil {
		t.Fatal(err)
	}
	clientIDs := []string{"", "6434095e-cc52-50de-99e7-e3878818b363", `"quoted" & 'single'`, "<script>+1</script>", "a\x00b", "emoji 👍 é", "\xff\xfe invalid", "&amp; already", "line\nbreak\ttab", "javascript:alert(1)", "{{.}}", "campfireQuickBoostClientID"}
	for i := range 256 {
		clientIDs = append(clientIDs, string(rune(i))+"x"+string(byte(i)))
	}
	for _, clientID := range clientIDs {
		for _, id := range []int64{0, 1, -7, 933434529, math.MaxInt64, quickBoostIDMarker} {
			var expected bytes.Buffer
			data := struct {
				ClientID string
				ID       int64
			}{clientID, id}
			if err := app.templates.ExecuteTemplate(&expected, "quick-boosts", data); err != nil {
				t.Fatal(err)
			}
			if actual := string(forms.render(clientID, id)); actual != expected.String() {
				t.Fatalf("client ID %q, ID %d:\n%s\nexpected\n%s", clientID, id, actual, expected.String())
			}
		}
	}
}
