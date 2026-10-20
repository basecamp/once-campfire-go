package database

import (
	"context"
	"database/sql"
)

// APIAuthor includes the role exposed by the message protocol, not credentials.
type APIAuthor struct {
	UserDisplay
	Role int
}

type MessageAPIData struct {
	Authors     map[int64]APIAuthor
	Mentions    map[int64]UserDisplay
	Attachments map[int64]string
}

// APIPage materializes page metadata in the same observation as scoped records.
// Link direction is separate: the released protocol chooses it from the presence
// of an after parameter, even when a nonzero before parameter selects the page.
func (r *MessageRead) APIPage(ctx context.Context, user, room, anchor int64, direction string, linkAfter bool) (records []Message, count int, next int64, err error) {
	refs, err := r.PageReferences(ctx, user, room, anchor, direction)
	if err != nil {
		return nil, 0, 0, err
	}
	records, err = r.Records(ctx, refs)
	if err != nil {
		return nil, 0, 0, err
	}
	if err = r.tx.QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE room_id=?", room).Scan(&count); err != nil {
		return nil, 0, 0, err
	}
	if len(records) != 0 {
		boundary := records[0]
		operator := "<"
		if linkAfter {
			boundary = records[len(records)-1]
			operator = ">"
		}
		var more bool
		err = r.tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE room_id=? AND created_at"+operator+"?)", room, Stamp(boundary.CreatedAt)).Scan(&more)
		if more {
			next = boundary.ID
		}
	}
	return
}

func (r *MessageRead) APIData(ctx context.Context, records []Message, mentioned []int64) (MessageAPIData, error) {
	return messageAPIData(ctx, r.tx, records, mentioned)
}

// MessageAPIData prepares captured records without claiming scoped provenance or
// caching them. Receipt bodies remain owned; current related rows share a snapshot.
func (d *DB) MessageAPIData(ctx context.Context, records []Message, mentioned []int64) (MessageAPIData, error) {
	read, err := d.BeginMessageRead(ctx)
	if err != nil {
		return MessageAPIData{}, err
	}
	defer read.Close()
	data, err := read.APIData(ctx, records, mentioned)
	if err != nil {
		return data, err
	}
	return data, read.Finish()
}

func messageAPIData(ctx context.Context, tx *sql.Tx, records []Message, mentioned []int64) (MessageAPIData, error) {
	data := MessageAPIData{Authors: map[int64]APIAuthor{}, Mentions: map[int64]UserDisplay{}, Attachments: map[int64]string{}}
	ids := map[int64]bool{}
	for _, record := range records {
		ids[record.CreatorID] = true
	}
	for _, id := range mentioned {
		ids[id] = true
	}
	if len(ids) == 0 {
		return data, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,coalesce(bio,''),updated_at,role FROM users WHERE id IN (SELECT value FROM json_each(?))", displayIDs(ids))
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var author APIAuthor
		if err = rows.Scan(&author.ID, &author.Name, &author.Bio, timestamp{&author.UpdatedAt}, &author.Role); err != nil {
			rows.Close()
			return data, err
		}
		data.Authors[author.ID] = author
		data.Mentions[author.ID] = author.UserDisplay
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return data, err
	}
	messages := map[int64]bool{}
	for _, record := range records {
		if _, ok := data.Authors[record.CreatorID]; !ok {
			return data, sql.ErrNoRows
		}
		messages[record.ID] = true
	}
	if len(messages) == 0 {
		return data, nil
	}
	// The API's filename fallback is best-effort, unlike required creator/mention
	// preparation. Select only the lowest valid attachment, preserving legacy data.
	rows, err = tx.QueryContext(ctx, "SELECT a.record_id,b.filename FROM active_storage_attachments a JOIN active_storage_blobs b ON b.id=a.blob_id WHERE a.record_type='Message' AND a.name='attachment' AND a.record_id IN (SELECT value FROM json_each(?)) AND a.id=(SELECT a2.id FROM active_storage_attachments a2 JOIN active_storage_blobs b2 ON b2.id=a2.blob_id WHERE a2.record_type='Message' AND a2.name='attachment' AND a2.record_id=a.record_id ORDER BY a2.id LIMIT 1)", displayIDs(messages))
	if err != nil {
		return data, nil
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var filename sql.NullString
		if err = rows.Scan(&id, &filename); err != nil {
			return data, nil
		}
		if filename.Valid {
			data.Attachments[id] = filename.String
		}
	}
	return data, nil
}
