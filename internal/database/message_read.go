package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"
)

// MessageReference identifies a selected record, not a partially loaded message.
type MessageReference struct {
	ID, RoomID int64
	UpdatedAt  time.Time
}

func (m Message) Reference() MessageReference {
	return MessageReference{ID: m.ID, RoomID: m.RoomID, UpdatedAt: m.UpdatedAt}
}

// MessageRead owns one short observation of scoped selection, bodies and display
// relationships. Finish all SQL and release it before presenting or transmitting.
type MessageRead struct {
	tx       *sql.Tx
	selected map[int64]MessageReference
}

func (d *DB) BeginMessageRead(ctx context.Context) (*MessageRead, error) {
	tx, err := d.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return &MessageRead{tx: tx}, nil
}

func (r *MessageRead) Close()        { _ = r.tx.Rollback() }
func (r *MessageRead) Finish() error { return r.tx.Commit() }

func (r *MessageRead) selectReferences(refs []MessageReference) []MessageReference {
	r.selected = make(map[int64]MessageReference, len(refs))
	for _, ref := range refs {
		r.selected[ref.ID] = ref
	}
	return refs
}

func (r *MessageRead) PageReferences(ctx context.Context, user, room, anchor int64, direction string) ([]MessageReference, error) {
	var member int
	if err := r.tx.QueryRowContext(ctx, "SELECT 1 FROM memberships WHERE user_id=? AND room_id=?", user, room).Scan(&member); err != nil {
		return nil, err
	}
	refs, err := messagePageReferences(ctx, r.tx, room, anchor, direction)
	if err != nil {
		return nil, err
	}
	return r.selectReferences(refs), nil
}

func (r *MessageRead) SearchReferences(ctx context.Context, user int64, query string) ([]MessageReference, error) {
	terms := searchTerms(query)
	if terms == "" {
		return r.selectReferences(nil), nil
	}
	refs, err := searchReferences(ctx, r.tx, user, terms)
	if err != nil {
		return nil, err
	}
	return r.selectReferences(refs), nil
}

// Records accepts only references selected by this observation. A miss cannot
// become unscoped ID hydration, or read a newer body that no longer matched.
func (r *MessageRead) Records(ctx context.Context, refs []MessageReference) ([]Message, error) {
	for _, ref := range refs {
		selected, ok := r.selected[ref.ID]
		if !ok || selected.RoomID != ref.RoomID || !selected.UpdatedAt.Equal(ref.UpdatedAt) {
			return nil, ErrForbidden
		}
	}
	return messageReferenceRecords(ctx, r.tx, refs)
}

func (r *MessageRead) Displays(ctx context.Context, records []Message, mentioned []int64) (map[int64]MessageDisplay, map[int64]UserDisplay, error) {
	return messageDisplays(ctx, r.tx, records, mentioned)
}

func scanMessageReferences(rows *sql.Rows) ([]MessageReference, error) {
	defer rows.Close()
	refs := []MessageReference{}
	for rows.Next() {
		var ref MessageReference
		if err := rows.Scan(&ref.ID, &ref.RoomID, timestamp{&ref.UpdatedAt}); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func messagePageReferences(ctx context.Context, tx *sql.Tx, room, anchor int64, direction string) ([]MessageReference, error) {
	var stamp string
	center := MessageReference{RoomID: room}
	if anchor != 0 {
		// Capture the anchor once. All three portions use this same observation and
		// preserve strict created_at boundaries: tied peers are not cursor substitutes.
		var updated any
		if err := tx.QueryRowContext(ctx, "SELECT id,created_at,updated_at FROM messages WHERE id=? AND room_id=?", anchor, room).Scan(&center.ID, &stamp, &updated); err != nil {
			return nil, err
		}
		if direction != "before" && direction != "after" {
			if err := (timestamp{&center.UpdatedAt}).Scan(updated); err != nil {
				return nil, err
			}
		}
	}
	var before, after []MessageReference
	if direction != "before" && anchor != 0 {
		rows, err := tx.QueryContext(ctx, "SELECT id,room_id,updated_at FROM messages WHERE room_id=? AND created_at>? ORDER BY created_at LIMIT 40", room, stamp)
		if err != nil {
			return nil, err
		}
		after, err = scanMessageReferences(rows)
		if err != nil {
			return nil, err
		}
		if direction == "after" {
			return after, nil
		}
	}
	query := "SELECT id,room_id,updated_at FROM messages WHERE room_id=? "
	args := []any{room}
	if anchor != 0 {
		query += "AND created_at<? "
		args = append(args, stamp)
	}
	rows, err := tx.QueryContext(ctx, query+"ORDER BY created_at DESC LIMIT 40", args...)
	if err != nil {
		return nil, err
	}
	before, err = scanMessageReferences(rows)
	if err != nil {
		return nil, err
	}
	slices.Reverse(before)
	if direction == "before" || anchor == 0 {
		return before, nil
	}
	return append(append(before, center), after...), nil
}

func messageReferenceRecords(ctx context.Context, tx *sql.Tx, refs []MessageReference) ([]Message, error) {
	if len(refs) == 0 {
		return []Message{}, nil
	}
	ids := make([]int64, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	raw, _ := json.Marshal(ids)
	rows, err := tx.QueryContext(ctx, messageSelect+"WHERE m.id IN (SELECT value FROM json_each(?))", string(raw))
	if err != nil {
		return nil, err
	}
	records, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Message, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	records = records[:0]
	for _, ref := range refs {
		record, ok := byID[ref.ID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		records = append(records, record)
	}
	return records, nil
}
