package views

import (
	"math"
	"strconv"
	"testing"
	"time"
)

func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestEpochTruncatesThroughAFloatLikeRuby(t *testing.T) {
	if got := EpochMS(parseTime(t, "2026-09-26T12:23:46.483521Z")); got != 1790425426483 {
		t.Errorf("got %d", got)
	}
	if got := EpochMS(parseTime(t, "2026-09-26T11:23:46Z")); got != 1790421826000 {
		t.Errorf("got %d", got)
	}
}

func TestFormatsNumbersLikeRuby(t *testing.T) {
	for _, c := range []struct {
		n    RubyNumber
		want string
	}{
		{RubyFloat(600.0), "600.0"},
		{RubyFloat(16.0 / 9.0), "1.7777777777777777"},
		{RubyFloat(1e15), "1.0e+15"},
		{RubyFloat(0.00001), "1.0e-05"},
		{RubyInt(641).Half(), "320"},
		{RubyInt(-3).Half(), "-2"},
	} {
		if got := c.n.String(); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestFormatsJSONTimesWithMilliseconds(t *testing.T) {
	if got := JSONTime(parseTime(t, "2026-09-26T12:26:46.848999Z")); got != "2026-09-26T12:26:46.848Z" {
		t.Errorf("got %s", got)
	}
	if got := ISO8601(parseTime(t, "2026-09-26T12:26:46.848999+02:00")); got != "2026-09-26T10:26:46Z" {
		t.Errorf("got %s", got)
	}
}

func TestFloatToSLikeRuby34(t *testing.T) {
	// Float#to_s in the reference (Ruby 3.4.10): from 1e15, the exponent form unless there are
	// digits after the decimal point; of two shortest forms equally close, the even one, as long as
	// it reads back.
	tenth, fifth := 0.1, 0.2 // added at run time, as doubles
	for _, c := range []struct {
		f    float64
		want string
	}{
		{100.0, "100.0"},
		{0.1, "0.1"},
		{0.0001, "0.0001"},
		{0.00012345, "0.00012345"},
		{1e-5, "1.0e-05"},
		{1.5e-7, "1.5e-07"},
		{5e-324, "5.0e-324"},
		{1e14, "100000000000000.0"},
		{123456789012345.6, "123456789012345.6"},
		{999999999999999.0, "999999999999999.0"},
		{999999999999999.9, "999999999999999.9"},
		{-999999999999999.0, "-999999999999999.0"},
		{1e15, "1.0e+15"},
		{-1e15, "-1.0e+15"},
		{1.5e15, "1.5e+15"},
		{1234567890123456.0, "1.234567890123456e+15"},
		{9007199254740992.0, "9.007199254740992e+15"},
		{1000000000000001.0, "1.000000000000001e+15"},
		{1963684456584958.8, "1963684456584958.8"},
		{1000000000000000.1, "1000000000000000.1"},
		{2251799813685248.5, "2251799813685248.5"},
		{-2551800308696183.5, "-2551800308696183.5"},
		{1e16, "1.0e+16"},
		{1e20, "1.0e+20"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
		{667020902720176.0 + 0.25, "667020902720176.2"},
		{667020902720176.0 + 0.75, "667020902720176.8"},
		{1000000000000000.2, "1000000000000000.2"},
		{1125899906842624.0 + 0.25, "1125899906842624.2"},
		{-(2074704973491874.0 + 0.25), "-2074704973491874.2"},
		{24603114260468.0 + 0.0625, "24603114260468.062"},
		{210745403561986.0 + 0.125, "210745403561986.12"},
		{math.Pow(2, -24), "5.960464477539063e-08"},
		{math.Pow(2, -25), "2.9802322387695312e-08"},
		{tenth + fifth, "0.30000000000000004"},
		{1.0 / 3.0, "0.3333333333333333"},
	} {
		if got := FloatToS(c.f); got != c.want {
			t.Errorf("FloatToS(%v) = %s, want %s", c.f, got, c.want)
		}
	}

	var vectors struct {
		Floats []struct {
			Bits string `json:"bits"`
			ToS  string `json:"to_s"`
		} `json:"floats"`
	}
	readVectors(t, &vectors)
	for _, c := range vectors.Floats {
		bits, err := strconv.ParseUint(c.Bits, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		f := math.Float64frombits(bits)
		if got := FloatToS(f); got != c.ToS {
			t.Errorf("FloatToS(%v) is %s, Ruby's %s", f, got, c.ToS)
		}
	}
}
