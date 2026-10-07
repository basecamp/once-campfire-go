package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode"
)

func SearchQuery(query string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsLetter(c) || unicode.IsNumber(c) || unicode.IsMark(c) || unicode.Is(unicode.Pc, c) {
			return c
		}
		return ' '
	}, query)
}

const searchProbeLimit = 1000

// SearchReferences follows the current Rails/Rust newest-ID search order. Bound
// the global FTS probe so sparse memberships cannot scan inaccessible history.
func (d *DB) SearchReferences(ctx context.Context, user int64, query string) ([]Message, error) {
	terms := searchTerms(query)
	if terms == "" {
		return []Message{}, nil
	}
	rows, err := d.Read.QueryContext(ctx, "SELECT m.id,m.room_id,m.updated_at,member.user_id IS NOT NULL FROM message_search_index idx JOIN messages m ON m.id=idx.rowid LEFT JOIN memberships member ON member.room_id=m.room_id AND member.user_id=? WHERE idx.body MATCH ? ORDER BY idx.rowid DESC LIMIT 1000", user, terms)
	if err != nil {
		return nil, err
	}
	messages, examined, err := searchReferenceRows(rows)
	if err != nil {
		return nil, err
	}
	if len(messages) < 100 && examined == searchProbeLimit {
		rows, err = d.Read.QueryContext(ctx, "SELECT m.id,m.room_id,m.updated_at,1 FROM messages m JOIN message_search_index idx ON idx.rowid=m.id JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND idx.body MATCH ? ORDER BY m.id DESC LIMIT 100", user, terms)
		if err != nil {
			return nil, err
		}
		messages, _, err = searchReferenceRows(rows)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, err
}

func searchReferenceRows(rows *sql.Rows) ([]Message, int, error) {
	defer rows.Close()
	messages := []Message{}
	examined := 0
	for rows.Next() {
		examined++
		var m Message
		var version any
		var reachable bool
		if err := rows.Scan(&m.ID, &m.RoomID, &version, &reachable); err != nil {
			return nil, examined, err
		}
		// Inaccessible data must not affect the caller, even if malformed.
		if reachable {
			if err := (timestamp{&m.UpdatedAt}).Scan(version); err != nil {
				return nil, examined, err
			}
			messages = append(messages, m)
			if len(messages) == 100 {
				break
			}
		}
	}
	return messages, examined, rows.Err()
}

// Hydrate only the selected page and recheck its membership in the same query.
func (d *DB) Search(ctx context.Context, user int64, query string) ([]Message, error) {
	refs, err := d.SearchReferences(ctx, user, query)
	if err != nil || len(refs) == 0 {
		return refs, err
	}
	ids := make([]int64, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := d.Read.QueryContext(ctx, messageSelect+"JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND m.id IN (SELECT value FROM json_each(?)) ORDER BY m.id", user, string(raw))
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

func searchTerms(query string) string {
	words := strings.Fields(SearchQuery(query))
	for i, word := range words {
		words[i] = "\"" + strings.ReplaceAll(word, "\"", "\"\"") + "\""
	}
	return strings.Join(words, " ")
}

func (d *DB) RecordSearch(ctx context.Context, user int64, query string) error {
	return d.Transaction(ctx, func(tx *sql.Tx) error {
		now := Stamp(d.Now())
		var id int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM searches WHERE user_id=? AND query=? LIMIT 1", user, query).Scan(&id)
		if err == sql.ErrNoRows {
			result, e := tx.ExecContext(ctx, "INSERT INTO searches(user_id,query,created_at,updated_at) VALUES (?,?,?,?)", user, query, now, now)
			if e != nil {
				return e
			}
			id, e = result.LastInsertId()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "DELETE FROM searches WHERE user_id=? AND id NOT IN (SELECT id FROM searches WHERE user_id=? ORDER BY updated_at DESC LIMIT 10)", user, user); e != nil {
				return e
			}
		} else if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE searches SET updated_at=? WHERE id=?", now, id)
		return err
	})
}
func (d *DB) RecentSearches(ctx context.Context, user int64) ([]string, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT query FROM searches WHERE user_id=? ORDER BY updated_at DESC", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	queries := []string{}
	for rows.Next() {
		var q string
		if err = rows.Scan(&q); err != nil {
			return nil, err
		}
		queries = append(queries, q)
	}
	return queries, rows.Err()
}
