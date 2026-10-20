package html

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
)

func tokenizerOutput(z *Tokenizer) ([]Token, error) {
	var tokens []Token
	for z.Next() != ErrorToken {
		tokens = append(tokens, z.Token())
	}
	return tokens, z.Err()
}

func TestTokenizerSizedAndStreamingReadersAgree(t *testing.T) {
	cases := []struct{ name, body, context string }{
		{"empty", "", "div"},
		{"text", "a", "div"},
		{"markup", `<!-- hi --><a title="a&amp;b" data-x='"'>😀 &lt; &#0;</a>`, "div"},
		{"raw context", `a<b &amp; c</script>`, "script"},
		{"malformed", `<p title="unfinished`, "div"},
	}
	for _, size := range []int{4095, 4096, 4097} {
		cases = append(cases, struct{ name, body, context string }{"buffer boundary", strings.Repeat("x", size), "div"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, wantErr := tokenizerOutput(NewTokenizerFragment(io.LimitReader(strings.NewReader(c.body), int64(len(c.body))), c.context))
			for _, reader := range []io.Reader{strings.NewReader(c.body), bytes.NewReader([]byte(c.body)), bytes.NewBufferString(c.body)} {
				got, err := tokenizerOutput(NewTokenizerFragment(reader, c.context))
				if err != wantErr || !reflect.DeepEqual(got, want) {
					t.Fatalf("%T: tokens or terminal error differ: %v / %v", reader, err, wantErr)
				}
			}
		})
	}
}

func TestTokenizerReaderCanGrowAfterConstruction(t *testing.T) {
	body := `<p title="a&amp;b">` + strings.Repeat("😀&amp;", 1000) + `</p>`
	var reader bytes.Buffer
	z := NewTokenizerFragment(&reader, "div")
	reader.WriteString(body)
	got, err := tokenizerOutput(z)
	want, wantErr := tokenizerOutput(NewTokenizerFragment(io.LimitReader(strings.NewReader(body), int64(len(body))), "div"))
	if err != wantErr || !reflect.DeepEqual(got, want) {
		t.Fatalf("input growth changed tokens or terminal error: %v / %v", err, wantErr)
	}
}
