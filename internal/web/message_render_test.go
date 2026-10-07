package web

import (
	"bytes"
	"html/template"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func TestMessageLayoutsMatchContextualTemplate(t *testing.T) {
	app, _, _, _ := testApp(t)
	for i, layout := range app.messageLayouts {
		if len(layout) == 0 {
			t.Fatalf("layout %d inactive: review slots and escaping after template changes", i)
		}
	}
	base := messageView{
		Message: database.Message{
			ID: 123, RoomID: 456, CreatorID: 789, ClientID: "client-id",
			Creator: "A & B", CreatedAt: time.Date(2026, 10, 6, 12, 30, 45, 987654321, time.UTC),
			UpdatedAt: time.Date(2026, 10, 6, 12, 31, 0, 123456789, time.UTC),
		},
		CreatorTitle: "Person <bio>", RoomName: "Room & name",
		HTML:             template.HTML("<p>trusted &amp; sanitized</p>"),
		Permalink:        "https://example.test/rooms/456/@123?x=1&y=2",
		CreatorUpdatedAt: time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC),
	}
	check := func(t *testing.T, v messageView) {
		t.Helper()
		expected, err := app.markup("message-uncached", v)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := app.messageMarkup(v)
		if err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			for i := range min(len(actual), len(expected)) {
				if actual[i] != expected[i] {
					t.Fatalf("byte %d: got %q; want %q", i,
						actual[max(0, i-30):min(len(actual), i+90)],
						expected[max(0, i-30):min(len(expected), i+90)])
				}
			}
			t.Fatalf("body length: got %d, want %d", len(actual), len(expected))
		}
	}
	// Exercise every compiled branch and each string's real text/attribute
	// contexts. The independent reference is the unchanged html/template.
	hostile := []string{
		"", "ordinary", "a+b", "\x00\"'><&=foo\t\r\n",
		"日本語 👩🏽‍💻 \u2028\u2029", "\xff\xfe\xc0\xaf\xe2\x82",
		"javascript:alert(1)", "JaVaScRiPt:alert(1)",
		"data:text/html,<script>alert(1)</script>", "//example.test/a b",
		"https://example.test/a b?x=\"\x00'<>é&y=+%zz%ff",
	}
	for branch := range 4 {
		for i, value := range hostile {
			t.Run(strings.Join([]string{string(rune('0' + branch)), string(rune('a' + i))}, "/"), func(t *testing.T) {
				v := base
				v.AllEmoji = branch&1 != 0
				v.ClientID, v.Creator, v.CreatorTitle, v.RoomName = value, value, value, value
				v.Permalink = value
				if branch&2 != 0 {
					v.Attachment = &storage.Blob{Filename: value}
					v.BlobURL, v.DownloadURL = value, value
				}
				check(t, v)
			})
		}
	}
	times := []time.Time{
		{}, time.Unix(-1, 999999999),
		time.Date(1960, 2, 3, 4, 5, 6, 789123456, time.FixedZone("offset", -7*3600)),
		time.Date(10000, 2, 3, 4, 5, 6, 123456789, time.UTC),
		time.Date(-500, 2, 3, 4, 5, 6, 123456789, time.UTC),
	}
	for i, timestamp := range times {
		t.Run("date/"+string(rune('a'+i)), func(t *testing.T) {
			v := base
			v.CreatedAt, v.UpdatedAt, v.CreatorUpdatedAt = timestamp, timestamp, timestamp
			v.ID, v.RoomID, v.CreatorID = math.MinInt64, math.MaxInt64, math.MinInt64
			check(t, v)
		})
	}
	t.Run("boost fallback", func(t *testing.T) {
		v := base
		v.Boosts = []database.Boost{{ID: 42, MessageID: v.ID, Content: "👍+<&", Booster: "name+<&"}}
		// Poison the compiled branch to prove boosts don't accidentally use it.
		saved := app.messageLayouts
		app.messageLayouts[0] = []messagePart{{text: "wrong", slot: -1}}
		defer func() { app.messageLayouts = saved }()
		check(t, v)
	})
	t.Run("disabled fallback", func(t *testing.T) {
		saved := app.messageLayouts
		app.messageLayouts = messageLayouts{}
		defer func() { app.messageLayouts = saved }()
		check(t, base)
	})
}

func TestMessageLayoutsRejectUnsupportedSource(t *testing.T) {
	source, err := templateFiles.ReadFile("templates/messages.html")
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.Replace(source, []byte(`class="message `), []byte(`class="different-message `), 1)
	// An unsupported source must decline compilation without touching even an
	// already executed tree; the caller retains the normal template renderer.
	original := template.Must(template.New("test").Parse("executed"))
	var output strings.Builder
	if err := original.Execute(&output, nil); err != nil {
		t.Fatal(err)
	}
	layouts, err := compileMessageLayouts(original, source)
	if err != nil {
		t.Fatal(err)
	}
	for i, layout := range layouts {
		if len(layout) != 0 {
			t.Fatalf("unsupported source compiled layout %d", i)
		}
	}
}

func FuzzMessageStringEscaping(f *testing.F) {
	for _, value := range []string{"plain", "a+b", "\x00\"'&<>", "\xff\xfe\xc0\xaf"} {
		f.Add(value)
	}
	original := template.Must(template.New("escape").Parse(`<div title="{{.}}">{{.}}</div>`))
	f.Fuzz(func(t *testing.T, value string) {
		var expected strings.Builder
		if err := original.Execute(&expected, value); err != nil {
			t.Fatal(err)
		}
		escaped := messageEscaper.Replace(value)
		if actual := `<div title="` + escaped + `">` + escaped + `</div>`; actual != expected.String() {
			t.Fatalf("escaping %q: got %q, want %q", value, actual, expected.String())
		}
	})
}
