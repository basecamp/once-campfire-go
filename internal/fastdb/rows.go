package fastdb

import (
	"fmt"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastdb/csqlite"
)

// Kind is a SQLite column value class. The values match sqlite3_column_type.
type Kind uint8

const (
	KindInteger Kind = iota + 1
	KindFloat
	KindText
	KindBlob
	KindNull
)

// Rows is a cursor over one statement execution. A Rows returned by a query
// is consumed by the caller and must not outlive that query; text accessors
// that copy (ColumnTextInto, Text) are safe to retain, the raw Bytes view is
// not.
type Rows struct {
	stmt *csqlite.Stmt
	err  error
}

// Next advances to the next row and reports whether one is available.
func (r *Rows) Next() bool {
	if r.err != nil {
		return false
	}
	row, err := r.stmt.Step()
	if err != nil {
		r.err = err
		return false
	}
	return row
}

// Err returns the first error encountered while stepping.
func (r *Rows) Err() error { return r.err }

// Kind returns column i's value class.
func (r *Rows) Kind(i int) Kind { return Kind(r.stmt.ColumnType(i)) }

// IsNull reports whether column i is SQL NULL.
func (r *Rows) IsNull(i int) bool { return r.stmt.ColumnType(i) == csqlite.TypeNull }

// Int64 returns column i as an integer. NULL reads as 0; use IsNull when the
// column is nullable.
func (r *Rows) Int64(i int) int64 { return r.stmt.ColumnInt64(i) }

// Bool returns column i as SQLite's boolean convention (0/1; NULL reads as
// false).
func (r *Rows) Bool(i int) bool { return r.stmt.ColumnInt64(i) != 0 }

// Bytes returns a view of column i's bytes owned by SQLite. The view is valid
// only until the next Next/Reset/Finalize; callers that must retain the value
// use ColumnTextInto.
func (r *Rows) Bytes(i int) []byte { return r.stmt.ColumnBytes(i) }

// ColumnTextInto appends column i's bytes to dst and returns the extended
// slice. The result is a copy owned by the caller (dst's backing array when it
// has room), so it survives the next row and the statement's reuse. NULL is
// ErrNull; an empty value appends nothing.
func (r *Rows) ColumnTextInto(i int, dst []byte) ([]byte, error) {
	if r.stmt.ColumnType(i) == csqlite.TypeNull {
		return dst, csqlite.ErrNull
	}
	return append(dst, r.stmt.ColumnBytes(i)...), nil
}

// Text returns column i as a freshly allocated string. NULL is ErrNull.
func (r *Rows) Text(i int) (string, error) { return r.stmt.ColumnText(i) }

// Stamp decodes column i as a timestamp. The bytes are decoded in place from
// SQLite's buffer (no copy, no allocation) and never retained.
func (r *Rows) Stamp(i int) (time.Time, error) {
	if r.stmt.ColumnType(i) == csqlite.TypeNull {
		return time.Time{}, csqlite.ErrNull
	}
	return decodeStamp(r.stmt.ColumnBytes(i))
}

// stampLayouts are exactly the layouts internal/database's timestamp.Scan
// accepts, in the same order, so a value decodes identically on both readers.
var stampLayouts = [...]string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05.999999999-07:00",
	time.RFC3339Nano,
}

// decodeStamp parses a timestamp the way internal/database does. The fast path
// handles the canonical "YYYY-MM-DD HH:MM:SS[.fraction]" form without
// allocating; anything else falls back to time.Parse with the database
// layouts, preserving its acceptance and error behaviour.
func decodeStamp(b []byte) (time.Time, error) {
	if len(b) >= 19 && b[4] == '-' && b[7] == '-' && b[10] == ' ' && b[13] == ':' && b[16] == ':' {
		if t, ok := decodeStampFast(b); ok {
			return t, nil
		}
	}
	for _, layout := range stampLayouts {
		if t, err := time.Parse(layout, string(b)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("fastdb: invalid timestamp %q", b)
}

// decodeStampFast parses the canonical fixed-width form time.Time-UTC. It
// returns false when the value is not exactly that form or when a component is
// out of range, leaving the authoritative parse (and its error) to
// time.Parse.
func decodeStampFast(b []byte) (time.Time, bool) {
	if len(b) != 19 && !(len(b) > 19 && len(b) <= 29 && b[19] == '.') {
		return time.Time{}, false
	}
	year, ok1 := atoiN(b, 0, 4)
	month, ok2 := atoiN(b, 5, 2)
	day, ok3 := atoiN(b, 8, 2)
	hour, ok4 := atoiN(b, 11, 2)
	minute, ok5 := atoiN(b, 14, 2)
	second, ok6 := atoiN(b, 17, 2)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return time.Time{}, false
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	nanos := 0
	if len(b) > 19 {
		digits := len(b) - 20
		if digits < 1 || digits > 9 {
			return time.Time{}, false
		}
		v, ok := atoiN(b, 20, digits)
		if !ok {
			return time.Time{}, false
		}
		for i := digits; i < 9; i++ {
			v *= 10
		}
		nanos = v
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.UTC)
	// time.Date normalises out-of-range values silently; reject those so the
	// fallback can return time.Parse's precise error (February 30, for one).
	if t.Year() != year || int(t.Month()) != month || t.Day() != day ||
		t.Hour() != hour || t.Minute() != minute || t.Second() != second ||
		t.Nanosecond() != nanos {
		return time.Time{}, false
	}
	return t, true
}

// atoiN parses exactly n ASCII digits at offset off.
func atoiN(b []byte, off, n int) (int, bool) {
	v := 0
	for i := 0; i < n; i++ {
		c := b[off+i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}
