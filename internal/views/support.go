package views

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Small Ruby/Rails behaviors the message and room views depend on (the reference's
// messages/support.rs): time formats and numbers as Ruby prints them.

// ISO8601 is time.iso8601 for a UTC ActiveSupport::TimeWithZone: seconds precision, Z suffix.
func ISO8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// EpochMS is time.to_fs(:epoch), defined in reference/config/initializers/time_formats.rb as
// `(time.to_f * 1000).to_i`. The float round trip is deliberate: it truncates some millisecond
// values down by one, and the client compares these numbers.
func EpochMS(t time.Time) int64 {
	// Time#to_f is the nearest double to the exact rational, which parsing the decimal
	// representation gives us.
	seconds, nanos := t.Unix(), t.Nanosecond()
	var decimal string
	if seconds < 0 && nanos != 0 {
		decimal = "-" + strconv.FormatInt(-seconds-1, 10) + "." + padNanos(1_000_000_000-nanos)
	} else {
		decimal = strconv.FormatInt(seconds, 10) + "." + padNanos(nanos)
	}
	toF, _ := strconv.ParseFloat(decimal, 64)
	return int64(toF * 1000)
}

// padNanos is nanos as nine digits.
func padNanos(nanos int) string {
	s := strconv.Itoa(nanos)
	return strings.Repeat("0", 9-len(s)) + s
}

// JSONTime is time.as_json with Active Support's default precision: 2026-09-26T12:26:46.848Z.
func JSONTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// RubyNumber is a number as Ruby prints it: integers bare, floats as Float#to_s writes them, always
// with a fractional part (600.0) and in exponent form outside 1e-4..1e15 (1.0e+15).
type RubyNumber struct {
	isFloat bool
	i       int64
	f       float64
}

func RubyInt(value int64) RubyNumber { return RubyNumber{i: value} }

func RubyFloat(value float64) RubyNumber { return RubyNumber{isFloat: true, f: value} }

func (n RubyNumber) IsFloat() bool { return n.isFloat }

func (n RubyNumber) ToF() float64 {
	if n.isFloat {
		return n.f
	}
	return float64(n.i)
}

// Half is number / 2: integer division (rounding down) for integers.
func (n RubyNumber) Half() RubyNumber {
	if n.isFloat {
		return RubyFloat(n.f / 2)
	}
	half := n.i / 2
	if n.i%2 < 0 {
		half--
	}
	return RubyInt(half)
}

func (n RubyNumber) String() string {
	if n.isFloat {
		return FloatToS(n.f)
	}
	return strconv.FormatInt(n.i, 10)
}

// FloatToS is Float#to_s: plain decimals from 1e-4 up to (not including) 1e15, and above that while
// the shortest digits still reach past the decimal point (1000000000000000.1); the exponent form
// otherwise (flo_to_s in Ruby 3.4's numeric.c).
func FloatToS(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	digits, decpt := shortestDigits(math.Abs(f))
	sign := ""
	if f < 0 {
		sign = "-"
	}
	switch {
	case decpt < -3 || decpt > 15 && len(digits) <= decpt:
		rest := digits[1:]
		if rest == "" {
			rest = "0"
		}
		e := decpt - 1
		exponentSign := "+"
		if e < 0 {
			exponentSign, e = "-", -e
		}
		exponent := strconv.Itoa(e)
		if e < 10 {
			exponent = "0" + exponent
		}
		return sign + digits[:1] + "." + rest + "e" + exponentSign + exponent
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	}
	return sign + digits[:decpt] + "." + digits[decpt:]
}

// shortestDigits is the shortest digits that read back as magnitude, and where the decimal point
// goes in them. When two such forms are equally close, Ruby's dtoa takes the even one:
// 667020902720176.25.to_s is "667020902720176.2". Those ties take 16 or 17 digits, and
// fixed-precision formatting breaks them to even.
func shortestDigits(magnitude float64) (string, int) {
	digits, decpt := scientificDigits(strconv.FormatFloat(magnitude, 'e', -1, 64))
	if len(digits) >= 16 && strings.IndexByte("13579", digits[len(digits)-1]) >= 0 {
		even := strconv.FormatFloat(magnitude, 'e', len(digits)-1, 64)
		if value, err := strconv.ParseFloat(even, 64); err == nil && value == magnitude {
			return scientificDigits(even)
		}
	}
	return digits, decpt
}

// scientificDigits is the digits of 1.2345e+06 and where the decimal point goes in them (7).
func scientificDigits(formatted string) (string, int) {
	mantissa, exponent, _ := strings.Cut(formatted, "e")
	e, _ := strconv.Atoi(exponent)
	return strings.Replace(mantissa, ".", "", 1), e + 1
}
