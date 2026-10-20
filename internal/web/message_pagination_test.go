package web

import (
	"database/sql"
	"math"
	"testing"
)

func TestMessageAnchorUsesActiveRecordIntegerCast(t *testing.T) {
	// Existing reference/crates/ruby/src/integer.rs vectors; zero is reserved
	// for an omitted query-layer cursor, not a supplied message identifier.
	for _, test := range []struct {
		value string
		want  int64
	}{
		{"12", 12}, {" +12abc", 12}, {"\t\n\v\f\r 7", 7},
		{"-3", -3}, {"0d12", 12}, {"0D12", 12}, {"0d0_5", 5},
		{"5_6", 56}, {"5__6", 5}, {"5\x00", 5},
		{"9223372036854775807", math.MaxInt64}, {"-9223372036854775808", math.MinInt64},
	} {
		got, err := messageAnchorID(test.value)
		if err != nil || got != test.want {
			t.Errorf("%q: got %d, %v; want %d", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{"", "0", "-0", "abc", "--5", "_5", "0x5", "0d_5", "\u00a05", "\x00 5", "9223372036854775808", "-9223372036854775809"} {
		if _, err := messageAnchorID(value); err != sql.ErrNoRows {
			t.Errorf("%q: got %v, want missing-message error", value, err)
		}
	}
}
