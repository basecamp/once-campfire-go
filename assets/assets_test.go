package assets

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestTurboCancellationOverride(t *testing.T) {
	const vendor = "../reference/crates/assets/vendor/turbo-rails/"
	original, err := os.ReadFile(vendor + "app/assets/javascripts/turbo.js")
	if err != nil {
		t.Fatal(err)
	}
	license, err := os.ReadFile(vendor + "MIT-LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	expected := string(original)
	for _, name := range []string{"requestSucceededWithResponse", "requestFailedWithResponse"} {
		call := "      this.delegate." + name + "(this, fetchResponse);"
		if strings.Count(expected, call) != 1 {
			t.Fatalf("pinned Turbo changed: expected one %s call; reconsider the override", name)
		}
		expected = strings.Replace(expected, call, "      await "+strings.TrimSpace(call), 1)
	}
	notice := "/*\nPort-owned Turbo 8.0.13 override from the pinned turbo-rails vendor.\n" +
		"Await response delegates so cancelled body reads reach the existing abort handler.\n\n" +
		string(license) + "\n*/\n"
	override, err := os.ReadFile("overrides/turbo.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(override, []byte(notice+expected)) {
		t.Fatal(
			"Turbo override must preserve the pinned library and license with only two added awaits",
		)
	}
	if read("generated/public/assets/"+Manifest["turbo.js"].Digested) != string(override) {
		t.Fatal("embedded Turbo differs from the override; rebuild assets")
	}
	if !strings.Contains(string(Importmap), Path("turbo.js")) {
		t.Fatal("import map does not load the overridden Turbo asset")
	}
}
