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
func (d *DB) RecordSearch(ctx context.Context, user int64, query string) error {
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
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
	if err == nil {
		d.changed()
	}
	return err
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
