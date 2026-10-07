package database

import (
	"context"
	"database/sql"
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

// SearchReferences returns reachable message IDs, room IDs and versions, with
// the latest 100 results in chronological order. Renderers hydrate cache misses.
func (d *DB) SearchReferences(ctx context.Context, user int64, query string) ([]Message, error) {
	terms := searchTerms(query)
	if terms == "" {
		return []Message{}, nil
	}
	rows, err := d.Read.QueryContext(ctx, "SELECT m.id,m.room_id,m.updated_at FROM messages m JOIN message_search_index idx ON idx.rowid=m.id JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND idx.body MATCH ? ORDER BY m.created_at DESC LIMIT 100", user, terms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.RoomID, timestamp{&m.UpdatedAt}); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, rows.Err()
}

// Search reads matching bodies in the same statement as result membership.
// It is the uncached path when a references-only result cannot reuse known bytes.
func (d *DB) Search(ctx context.Context, user int64, query string) ([]Message, error) {
	terms := searchTerms(query)
	if terms == "" {
		return []Message{}, nil
	}
	rows, err := d.Read.QueryContext(ctx, messageSelect+"JOIN message_search_index idx ON idx.rowid=m.id JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND idx.body MATCH ? ORDER BY m.created_at DESC LIMIT 100", user, terms)
	if err != nil {
		return nil, err
	}
	messages, err := scanMessages(rows)
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, err
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
