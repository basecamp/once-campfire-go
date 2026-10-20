package richtext

import (
	"reflect"
	"strings"
	"testing"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
	"golang.org/x/net/html/atom"
)

// The literal-text shortcut must produce the parser's tree, not just equivalent
// display text. Non-body contexts and special input retain HTML parsing.
func TestFragmentTextMatchesParser(t *testing.T) {
	bodies := []string{"", "hello", " \t\n\v\f ", "\ufefftext", "a>b", "é\u00a0🙂", "a\r\nb", "a\x00b", string([]byte{0xff}), "&amp;", "<b>text</b>", "<table>text<tr><td>cell", strings.Repeat("text ", 10000)}
	contexts := []*xhtml.Node{nil, {Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}, {Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}, {Type: xhtml.ElementNode, Data: "table", DataAtom: atom.Table}, {Type: xhtml.ElementNode, Data: "textarea", DataAtom: atom.Textarea}, {Type: xhtml.ElementNode, Data: "script", DataAtom: atom.Script}}
	for _, context := range contexts {
		for _, body := range bodies {
			got, err := parseIn(body, context)
			if err != nil {
				t.Fatal(err)
			}
			reference := context
			if reference == nil {
				reference = &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
			}
			nodes, err := xhtml.ParseFragmentWithOptions(strings.NewReader(strings.TrimPrefix(body, "\ufeff")), reference, xhtml.ParseOptionEnableScripting(false))
			if err != nil {
				t.Fatal(err)
			}
			want := &xhtml.Node{Type: xhtml.DocumentNode}
			for _, n := range nodes {
				want.AppendChild(n)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fragment differs from parser: context=%v body=%q", context, body)
			}
		}
	}
}
