package fastdb

import (
	"encoding/json"
	"strings"
	"unicode"
)

// The SQL below mirrors internal/database/search.go after the upstream merge
// (PR #9's newest-ID search order plus shared-verification updates):
//
//   - querySearchProbe is SearchReferences' bounded global FTS probe: the
//     1000 newest matches by rowid with the caller's memberships LEFT-joined,
//     so sparse memberships never scan inaccessible history.
//   - querySearchFallback is the membership-scoped refresh probe run when the
//     global probe was exhausted (examined == 1000) with fewer than 100
//     reachable hits: it scans the caller's own memberships directly, newest
//     by insertion id, capped at 100.
//   - querySearchHydrate is Search's recheck-and-hydrate pass: it re-selects
//     exactly the chosen ids joined to the caller's memberships (a permission
//     recheck in the same query) and orders chronologically by m.id, so rows
//     whose membership disappeared between selection and hydration drop out.
//
// The differential test holds the search results field-equal to database.DB.Search.

const querySearchProbe = "SELECT m.id,m.room_id,m.updated_at,member.user_id IS NOT NULL FROM message_search_index idx JOIN messages m ON m.id=idx.rowid LEFT JOIN memberships member ON member.room_id=m.room_id AND member.user_id=? WHERE idx.body MATCH ? ORDER BY idx.rowid DESC LIMIT 1000"

const querySearchFallback = "SELECT m.id,m.room_id,m.updated_at,1 FROM messages m JOIN message_search_index idx ON idx.rowid=m.id JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND idx.body MATCH ? ORDER BY m.id DESC LIMIT 100"

const querySearchHydrate = messageSelect + "JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND m.id IN (SELECT value FROM json_each(?)) ORDER BY m.id"

// searchProbeLimit mirrors database.searchProbeLimit: the number of FTS
// matches the global probe examines before the membership-scoped fallback
// may run.
const searchProbeLimit = 1000

// Search mirrors database.DB.Search (internal/database/search.go): the word
// fold, the quoted-token MATCH expression, the bounded global probe with the
// membership-scoped fallback, the newest-100 selection by insertion id, the
// reversal to chronological order and the hydration pass that rechecks the
// caller's membership in the same query. Appends to dst (pass dst[:0] to
// reuse a buffer); an empty result is a non-nil empty slice exactly like the
// database reader, which returns []Message{} before touching SQLite for a
// query with no words.
//
// Error shapes mirror the database reader: a probe failure returns (nil, err)
// like SearchReferences' early return; the hydrate pass drops the partial
// slice and returns (nil, err) on a decode error and the partial slice on a
// step error, exactly like scanMessages. The MATCH expression and the id
// list are built per call — they are request state, not per-record state, so
// the couple of allocations they cost are off the scan itself.
func (c *Conn) Search(dst []Message, user int64, query string) ([]Message, error) {
	terms := searchTerms(query)
	if terms == "" {
		out := dst
		if out == nil {
			out = []Message{}
		}
		return out, nil
	}
	refs, err := c.searchRefs(user, terms)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		out := dst
		if out == nil {
			out = []Message{}
		}
		return out, nil
	}
	ids := make([]int64, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	h, err := c.acquire(querySearchHydrate)
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
	if err := h.st.BindText(2, string(raw)); err != nil {
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
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// searchRefs mirrors database.DB.SearchReferences: the bounded global probe
// keeps the first 100 reachable (membership-joined) matches, and when the
// probe was exhausted without reaching 100 the membership-scoped fallback
// re-scans the caller's own memberships. The selected references come back in
// chronological insertion order — the SQL scans newest-first and the slice is
// reversed exactly like the database reader. A probe failure returns (nil,
// err) like SearchReferences' early return.
func (c *Conn) searchRefs(user int64, terms string) ([]MessageRef, error) {
	refs, examined, err := c.searchRefRows(querySearchProbe, user, terms)
	if err != nil {
		return nil, err
	}
	if len(refs) < 100 && examined == searchProbeLimit {
		refs, _, err = c.searchRefRows(querySearchFallback, user, terms)
		if err != nil {
			return nil, err
		}
	}
	for i, j := 0, len(refs)-1; i < j; i, j = i+1, j-1 {
		refs[i], refs[j] = refs[j], refs[i]
	}
	return refs, nil
}

// searchRefRows runs one reference scan: the id, room and update stamp of
// each match, with the membership flag the probe LEFT-joins (the fallback
// hard-codes it true). Inaccessible rows are scanned but not collected, and —
// like searchReferenceRows — their stamps are not decoded at all; a malformed
// stamp in a reachable row is an error that drops the partial slice. The
// return is the collected references (capped at 100, as the readers do), the
// number of rows examined, and the scan error, if any.
func (c *Conn) searchRefRows(query string, user int64, terms string) ([]MessageRef, int, error) {
	h, err := c.acquire(query)
	if err != nil {
		return nil, 0, err
	}
	defer h.release()
	if err := h.st.ClearBindings(); err != nil {
		return nil, 0, err
	}
	if err := h.st.BindInt64(1, user); err != nil {
		return nil, 0, err
	}
	if err := h.st.BindText(2, terms); err != nil {
		return nil, 0, err
	}
	rows := Rows{stmt: h.st}
	refs := []MessageRef{}
	examined := 0
	for rows.Next() {
		examined++
		var ref MessageRef
		var err error
		if ref.ID, err = rows.Int64(0); err != nil {
			return nil, examined, err
		}
		if ref.RoomID, err = rows.Int64(1); err != nil {
			return nil, examined, err
		}
		if !rows.Bool(3) {
			continue
		}
		if ref.UpdatedAt, err = rows.Stamp(2); err != nil {
			return nil, examined, err
		}
		refs = append(refs, ref)
		if len(refs) == 100 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return refs, examined, err
	}
	return refs, examined, nil
}

// searchTerms mirrors database.searchTerms: every word folded by searchQuery
// becomes a quoted token with embedded quotes doubled, joined by spaces.
func searchTerms(query string) string {
	words := strings.Fields(searchQuery(query))
	for i, word := range words {
		words[i] = "\"" + strings.ReplaceAll(word, "\"", "\"\"") + "\""
	}
	return strings.Join(words, " ")
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
