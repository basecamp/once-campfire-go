package views

import "testing"

func TestSearchPathsEscapeTheQueryLikeCGIEscape(t *testing.T) {
	if got := SearchPath(`pizza & "pie" *~`); got != "/searches?q=pizza+%26+%22pie%22+%2A~" {
		t.Errorf("got %s", got)
	}
}
