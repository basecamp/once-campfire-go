package richtext

import (
	"testing"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

func TestPlainListContextAndSubtrees(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
	}{
		{"nested", `<ol><li>A</li><li>B<ul><li>C</li><li>D</li></ul></li><li>E</li></ol>`, "1. A\n2. B\n  • C\n  • D\n3. E"},
		{"element siblings", `<ol><div>lead</div><!-- comment --><li>A</li>text<li>B</li></ol>`, "lead\n2. A\ntext3. B"},
		{"subtree ordinal", `<ol><div>lead</div><li>A</li><!-- comment --><li id="target">B</li></ol>`, "3. B"},
		{"subtree depth", `<ul><li>X<ol id="target"><li>A</li><li>B</li></ol></li></ul>`, "\n  1. A\n  2. B"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := parse(test.body)
			if err != nil {
				t.Fatal(err)
			}
			selected := root
			walk(root, func(node *xhtml.Node) {
				if attr(node, "id") == "target" {
					selected = node
				}
			})
			if got := chomp(plain(selected)); got != test.want {
				t.Fatalf("plaintext %q, want %q", got, test.want)
			}
		})
	}
}
