package fastdb

import (
	"strings"
	"unicode"
)

// querySearch mirrors database.DB.Search exactly: the full message select
// joined to the FTS index on the message rowid and to the caller's
// memberships, newest first, capped at 100 rows.
const querySearch = messageSelect + "JOIN message_search_index idx ON idx.rowid=m.id JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND idx.body MATCH ? ORDER BY m.created_at DESC LIMIT 100"

// Search mirrors database.DB.Search (internal/database/models.go): the word
// fold, the quoted-token MATCH expression, the membership-scoped FTS scan, the
// newest-first LIMIT 100 and the reversal to chronological order. Appends to
// dst (pass dst[:0] to reuse a buffer); an empty result is a non-nil empty
// slice exactly like the database reader, which returns []Message{} before
// touching SQLite for a query with no words.
//
// Error shapes mirror the database reader: a decode error drops the partial
// slice and returns (nil, err); a step error returns the partial slice,
// reversed. The MATCH expression is built per call — it is request state, not
// per-record state, so the couple of allocations it costs are off the scan
// itself.
func (c *Conn) Search(dst []Message, user int64, query string) ([]Message, error) {
	words := strings.Fields(searchQuery(query))
	if len(words) == 0 {
		out := dst
		if out == nil {
			out = []Message{}
		}
		return out, nil
	}
	for i, w := range words {
		words[i] = "\"" + strings.ReplaceAll(w, "\"", "\"\"") + "\""
	}
	h, err := c.acquire(querySearch)
	if err != nil {
		return nil, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return nil, err
	}
	if err := h.st.BindInt64(1, user); err != nil {
		return nil, err
	}
	if err := h.st.BindText(2, strings.Join(words, " ")); err != nil {
		return nil, err
	}
	rows := Rows{stmt: h.st}
	out := dst
	if out == nil {
		out = []Message{}
	}
	for rows.Next() {
		message, err := scanMessage(&rows)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	// The SQL scans newest-first; database.Search reverses the full slice
	// before returning rows.Err(), and only the appended segment is reversed
	// here.
	for i, j := len(dst), len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// searchQuery mirrors database.SearchQuery (internal/database/search.go):
// every rune that is not a letter, number, mark or connector punctuation
// (unicode.Pc) folds to a space, so the MATCH expression never carries
// operator syntax. The differential test holds the two functions byte-equal
// over a corpus of queries, the same way the SQL text is held to the mirrored
// rows.
func searchQuery(query string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsLetter(c) || unicode.IsNumber(c) || unicode.IsMark(c) || unicode.Is(unicode.Pc, c) {
			return c
		}
		return ' '
	}, query)
}
