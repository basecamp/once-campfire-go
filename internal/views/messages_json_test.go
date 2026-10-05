package views

import "testing"

// Escapes as ActiveSupport::JSON.encode: <, > and & as \u003c, \u003e and \u0026, U+2028 and
// U+2029 left alone, a backslash before "u2028" kept as an escaped backslash.
func TestEncodesJSONLikeActiveSupport(t *testing.T) {
	u := func(hex string) string { return `\` + "u" + hex }
	separators := string(rune(0x2028)) + string(rune(0x2029))
	got := railsJSON([]any{"<a href='x'>&</a>", separators, `\` + "u2028", "\x01\n\x7f"})
	want := `["` + u("003c") + `a href='x'` + u("003e") + u("0026") + u("003c") + `/a` + u("003e") + `","` +
		separators + `","\\u2028","` + u("0001") + `\n` + "\x7f" + `"]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := MessagesByBotsIndexJSON(nil); got != "[]" {
		t.Errorf("got %s", got)
	}
}

func TestManifestStringsAreJSONLikeSerdeJSON(t *testing.T) {
	u := func(hex string) string { return `\` + "u" + hex }
	value := "a\"b\\c\b\f\n\r\t\x01\x1f<&>" + string(rune(0x2028)) + "é"
	want := `"a\"b\\c\b\f\n\r\t` + u("0001") + u("001f") + "<&>" + string(rune(0x2028)) + `é"`
	if got := ManifestJSON(value); string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
