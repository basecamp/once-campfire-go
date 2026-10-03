package httpcompat

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRackRangeVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/rack_ranges.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Header string
		Size   int64
		Ranges [][2]int64
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 39 {
		t.Fatalf("missing vectors: %d", len(cases))
	}
	for _, c := range cases {
		if got := ByteRanges(c.Header, c.Size); !reflect.DeepEqual(got, c.Ranges) {
			t.Errorf("%q size %d: got %v want %v", c.Header, c.Size, got, c.Ranges)
		}
	}
	if got := ByteRanges("bytes=0-1,"+strings.Repeat("0-0,", 98), 10); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
	if got := ByteRanges("bytes=0-1,"+strings.Repeat("0-0,", 99), 10); got != nil {
		t.Fatal(got)
	}
}
