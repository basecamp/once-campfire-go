package web

import (
	"database/sql"
	"math"
	"net/http"
	"strings"
)

// Page selection prefers a present before parameter; the API Link independently
// follows a present after parameter. Both consume normalized request parameters.
func messagePagination(r *http.Request) (anchor int64, direction string, linkAfter bool, err error) {
	before, after := r.Form.Get("before"), r.Form.Get("after")
	linkAfter = strings.TrimSpace(after) != ""
	value, direction := before, "before"
	if strings.TrimSpace(value) == "" {
		value, direction = after, "after"
	}
	if strings.TrimSpace(value) == "" {
		return 0, direction, linkAfter, nil
	}
	anchor, err = messageAnchorID(value)
	return
}

// Active Record accepts a signed decimal prefix (including 0d and inter-digit
// underscores), not just strconv's complete integer syntax. Invalid/zero IDs must
// not turn into the query layer's zero sentinel for an omitted cursor.
func messageAnchorID(value string) (int64, error) {
	value = strings.TrimLeft(value, " \t\r\n\v\f")
	negative := strings.HasPrefix(value, "-")
	if strings.HasPrefix(value, "+") || negative {
		value = value[1:]
	}
	if len(value) == 0 || value[0] < '0' || value[0] > '9' {
		return 0, sql.ErrNoRows
	}
	if strings.HasPrefix(value, "0d") || strings.HasPrefix(value, "0D") {
		value = value[2:]
	}
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}
	var number uint64
	previousDigit := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '_' && previousDigit {
			previousDigit = false
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		digit := uint64(c - '0')
		if number > (limit-digit)/10 {
			return 0, sql.ErrNoRows
		}
		number = number*10 + digit
		previousDigit = true
	}
	id := int64(number)
	if negative {
		id = -id
	}
	if id == 0 {
		return 0, sql.ErrNoRows
	}
	return id, nil
}
