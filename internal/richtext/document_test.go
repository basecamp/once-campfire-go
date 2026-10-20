package richtext

import (
	"html"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestContentKeepsPreparedDocumentReusable(t *testing.T) {
	mention := Mention{ID: 7, Name: "Person", SGID: "canonical", Path: "/users/7", Avatar: "/avatar"}
	ctx := Context{Resolve: func(string, bool) (*Mention, error) { return &mention, nil }}
	for _, test := range []struct{ body, plain string }{
		{`<p>Hello &amp; goodbye</p><script>not plaintext</script>`, "Hello & goodbye"},
		{`<p>Hello <action-text-attachment sgid="user"></action-text-attachment></p>`, "Hello @Person"},
	} {
		doc := Prepare(test.body)
		before, err := doc.Display(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, err := doc.Content(ctx)
		if err != nil || want.Plain != test.plain {
			t.Fatalf("content %+v (%v), want plaintext %q", want, err, test.plain)
		}
		var work sync.WaitGroup
		for range 8 {
			work.Go(func() {
				got, err := doc.Content(ctx)
				if err != nil || got.Plain != want.Plain || got.BodyHTML != want.BodyHTML {
					t.Errorf("reused content changed: %+v (%v)", got, err)
				}
			})
		}
		work.Wait()
		after, err := doc.Display(ctx)
		if err != nil || after.Presentation != before.Presentation || after.Plain != before.Plain || after.Filtered != before.Filtered {
			t.Fatal("content rendering changed later display", after, err)
		}
	}
}

func TestPreparedNestedMentionsUseRenderingSanitization(t *testing.T) {
	for _, trix := range []string{`{"caption":"x"}`, `["invalid trix merge"]`} {
		nested := `<action-text-attachment sgid="nested-user" data-trix-attachment='` + trix + `'></action-text-attachment>`
		body := `<action-text-attachment content-type="text/html" content="` + html.EscapeString(nested) + `"></action-text-attachment>`
		doc := Prepare(body)
		tokens := doc.Attachables()
		if !slices.Equal(tokens, []string{"nested-user"}) {
			t.Fatalf("nested token lost after sanitization: %v", tokens)
		}
		ctx := Context{Resolve: func(token string, _ bool) (*Mention, error) {
			if !slices.Contains(tokens, token) {
				return nil, nil
			}
			return &Mention{ID: 7, Name: "Mentioned", SGID: token, Path: "/users/7", Avatar: "/avatar"}, nil
		}}
		first, err := doc.Display(ctx)
		if err != nil || !strings.Contains(first.Presentation, "Mentioned") {
			t.Fatalf("nested mention lost: %+v %v", first, err)
		}
		second, err := doc.Display(ctx)
		if err != nil || first.Presentation != second.Presentation {
			t.Fatal("prepared tree was mutated", err)
		}
	}
}
