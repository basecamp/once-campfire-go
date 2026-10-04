package web

import (
	"bytes"
	"html/template"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func randomMessageViews(n int) []messageView {
	pieces := []string{"", "a", "Z", "0", " ", "<", ">", "&", "&amp;", "\"", "'", "+", "\x00", "\xff", "é", "👍", "❤️", "%", "%41", "%zz", ":", "/", "?", "#", "=", "javascript:", "JavaScript:alert(1)", "http:", "HTTPS://", "mailto:", "//", "data:", "\n", "\t", "\u2028", "<script>", "{{.}}", "ZgotmplZ", "~", "[", "]", "(", ")", "`"}
	rng := rand.New(rand.NewPCG(7, 11))
	text := func() string {
		var b strings.Builder
		for range rng.IntN(6) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		return b.String()
	}
	moment := func() time.Time {
		switch rng.IntN(4) {
		case 0:
			return time.Time{}
		case 1:
			return time.Date(2026, 3, 2, 16, 0, 0, 0, time.UTC)
		default:
			return time.Unix(rng.Int64N(1<<34)-1<<33, rng.Int64N(1e9)).In(time.FixedZone("z", rng.IntN(50000)-25000))
		}
	}
	id := func() int64 { return rng.Int64N(1<<40) - 1<<20 }
	views := make([]messageView, n)
	for i := range views {
		v := messageView{Message: database.Message{ID: id(), RoomID: id(), CreatorID: id(), ClientID: text(), Creator: text(), CreatedAt: moment(), UpdatedAt: moment()},
			AllEmoji: rng.IntN(2) == 0, HTML: template.HTML(text()), Permalink: text(), CreatorTitle: text(), CreatorUpdatedAt: moment(), RoomName: text()}
		if rng.IntN(3) == 0 {
			v.Attachment = &storage.Blob{Filename: text()}
			v.BlobURL, v.DownloadURL = text(), text()
		}
		for range rng.IntN(4) {
			content := text()
			if rng.IntN(3) == 0 {
				content = []string{"👍", "🎉🔥", "❤️"}[rng.IntN(3)]
			}
			v.Boosts = append(v.Boosts, database.Boost{ID: id(), MessageID: id(), BoosterID: id(), Content: content, Booster: text(), BoosterTitle: text(), BoosterUpdatedAt: moment()})
		}
		views[i] = v
	}
	return views
}

func TestMessageRendererMatchesTemplate(t *testing.T) {
	app, _, _, _ := testApp(t)
	for _, view := range randomMessageViews(20_000) {
		var expected bytes.Buffer
		if err := app.templates.ExecuteTemplate(&expected, "message-uncached", view); err != nil {
			t.Fatal(err)
		}
		if actual := app.renderer.render(view); actual != expected.String() {
			i := 0
			for i < len(actual) && i < expected.Len() && actual[i] == expected.String()[i] {
				i++
			}
			t.Fatalf("%+v\ndiffers at %d:\n%q\nexpected\n%q", view, i, actual[max(0, i-80):min(len(actual), i+80)], expected.String()[max(0, i-80):min(expected.Len(), i+80)])
		}
	}
}

func TestURLAttributeMatchesTemplate(t *testing.T) {
	tmpl := template.Must(template.New("").Parse(`<a href="{{.}}">`))
	var inputs []string
	for i := range 256 {
		inputs = append(inputs, string([]byte{byte(i)}), "/a"+string([]byte{byte(i)})+"b", "%"+string([]byte{byte(i)}))
	}
	inputs = append(inputs, "javascript:x", "JAVASCRIPT:x", "http:x", "HtTpS://a", "mailto:a", "/a:b", "a/b:c", ":x", "tel:1", "%4", "%4G", "%41%", "é/👍")
	for _, input := range inputs {
		var expected bytes.Buffer
		if err := tmpl.Execute(&expected, input); err != nil {
			t.Fatal(err)
		}
		if actual := `<a href="` + urlAttribute(input) + `">`; actual != expected.String() {
			t.Fatalf("%q: %q, expected %q", input, actual, expected.String())
		}
	}
}
