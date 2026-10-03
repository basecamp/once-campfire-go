package qrcode

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReferenceQRCodes(t *testing.T) {
	raw, err := os.ReadFile("../../reference/crates/campfire/src/controllers/qr_code/testdata/rqrcode.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Input   string `json:"input_base64"`
		Version int
		Modules string
		SVG     *string
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		data, err := base64.StdEncoding.DecodeString(c.Input)
		if err != nil {
			t.Fatal(err)
		}
		modules, version := Modules(data)
		var lines []string
		for _, row := range modules {
			var line strings.Builder
			for _, dark := range row {
				if dark {
					line.WriteByte('1')
				} else {
					line.WriteByte('0')
				}
			}
			lines = append(lines, line.String())
		}
		if version != c.Version || strings.Join(lines, "\n") != c.Modules {
			t.Errorf("case %d: matrix/version mismatch (%d/%d)", i, version, c.Version)
		}
		if c.SVG != nil {
			svg, ok := SVG(data)
			if !ok || svg != *c.SVG {
				t.Errorf("case %d: SVG mismatch", i)
			}
		}
	}
	if _, ok := SVG([]byte(strings.Repeat("a", 3000))); ok {
		t.Fatal("oversized code accepted")
	}
}
