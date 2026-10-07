package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"
)

// ReachableMessage applies the same membership scope used by message and boost controllers.
func (d *DB) ReachableMessage(ctx context.Context, user, id int64) (Message, error) {
	rows, err := d.Read.QueryContext(ctx, messageSelect+"JOIN memberships member ON member.room_id=m.room_id WHERE member.user_id=? AND m.id=?", user, id)
	if err != nil {
		return Message{}, err
	}
	messages, err := scanMessages(rows)
	if err != nil {
		return Message{}, err
	}
	if len(messages) == 0 {
		return Message{}, sql.ErrNoRows
	}
	return messages[0], nil
}

func (d *DB) MessagePage(ctx context.Context, room, anchor int64, direction string) ([]Message, error) {
	if direction == "before" || anchor == 0 {
		return d.Messages(ctx, room, anchor)
	}
	var stamp string
	if err := d.Read.QueryRowContext(ctx, "SELECT created_at FROM messages WHERE id=? AND room_id=?", anchor, room).Scan(&stamp); err != nil {
		return nil, err
	}
	rows, err := d.Read.QueryContext(ctx, messageSelect+"WHERE m.room_id=? AND m.created_at>? ORDER BY m.created_at LIMIT 40", room, stamp)
	if err != nil {
		return nil, err
	}
	after, err := scanMessages(rows)
	if err != nil || direction == "after" {
		return after, err
	}
	before, err := d.Messages(ctx, room, anchor)
	if err != nil {
		return nil, err
	}
	rows, err = d.Read.QueryContext(ctx, messageSelect+"WHERE m.room_id=? AND m.id=?", room, anchor)
	if err != nil {
		return nil, err
	}
	center, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	return append(append(before, center...), after...), nil
}

func (d *DB) RefreshedMessages(ctx context.Context, room int64, since time.Time) (created, updated []Message, err error) {
	rows, err := d.Read.QueryContext(ctx, messageSelect+"WHERE m.room_id=? AND m.created_at>? ORDER BY m.created_at LIMIT 40", room, Stamp(since))
	if err != nil {
		return
	}
	created, err = scanMessages(rows)
	if err != nil {
		return
	}
	rows, err = d.Read.QueryContext(ctx, messageSelect+"WHERE m.room_id=? AND m.updated_at>? ORDER BY m.created_at DESC LIMIT 40", room, Stamp(since))
	if err != nil {
		return
	}
	updated, err = scanMessages(rows)
	if err != nil {
		return
	}
	slices.Reverse(updated)
	ids := make(map[int64]bool, len(created))
	for _, m := range created {
		ids[m.ID] = true
	}
	updated = slices.DeleteFunc(updated, func(m Message) bool { return ids[m.ID] })
	return
}

func messagePermission(ctx context.Context, tx *sql.Tx, user, id int64, administer bool) (room int64, err error) {
	var creator int64
	var role int
	err = tx.QueryRowContext(ctx, "SELECT m.room_id,m.creator_id,u.role FROM messages m JOIN memberships member ON member.room_id=m.room_id JOIN users u ON u.id=member.user_id WHERE m.id=? AND u.id=? AND u.status=0", id, user).Scan(&room, &creator, &role)
	if err == nil && administer && creator != user && role != 1 {
		err = ErrForbidden
	}
	return
}
func touchMessage(ctx context.Context, tx *sql.Tx, id, room int64, now string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE messages SET updated_at=? WHERE id=?", now, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=?", now, room)
	return err
}
func (d *DB) UpdateMessage(ctx context.Context, user, id int64, body, plain string) (Message, error) {
	return d.UpdateMessageAttributes(ctx, user, id, &body, plain, nil)
}

// A nil body or attachment leaves that attribute unchanged; attachment zero removes it.
// Body, attachment, timestamps and the search index commit together.
func (d *DB) UpdateMessageAttributes(ctx context.Context, user, id int64, body *string, plain string, attachment *int64) (Message, error) {
	return d.UpdateMessageWithUpload(ctx, user, id, body, plain, attachment, nil)
}
func (d *DB) UpdateMessageWithUpload(ctx context.Context, user, id int64, body *string, plain string, attachment *int64, staged BlobStager) (Message, error) {
	if staged != nil {
		defer staged.Discard()
	}
	var purged []int64
	var touched bool
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		room, err := messagePermission(ctx, tx, user, id, true)
		if err != nil {
			return err
		}
		now := Stamp(d.Now())
		changed := false
		if body != nil {
			var old sql.NullString
			err = tx.QueryRowContext(ctx, "SELECT body FROM action_text_rich_texts WHERE record_type='Message' AND record_id=? AND name='body'", id).Scan(&old)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == sql.ErrNoRows {
				_, err = tx.ExecContext(ctx, "INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)", id, *body, now, now)
				changed = true
			} else if !old.Valid || old.String != *body {
				_, err = tx.ExecContext(ctx, "UPDATE action_text_rich_texts SET body=?,updated_at=? WHERE record_type='Message' AND record_id=? AND name='body'", *body, now, id)
				changed = true
			}
			if err != nil {
				return err
			}
		}
		if staged != nil {
			blob, err := staged.Insert(ctx, tx)
			if err != nil {
				return err
			}
			attachment = &blob
		}
		if attachment != nil {
			purged, err = AttachmentBlobIDs(ctx, tx, "record_type='Message' AND record_id=? AND name='attachment'", id)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type='Message' AND record_id=? AND name='attachment'", id); err != nil {
				return err
			}
			if *attachment != 0 {
				if _, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'Message',?,'attachment',?)", *attachment, id, now); err != nil {
					return err
				}
			}
			changed = changed || len(purged) > 0 || *attachment != 0
		}
		if !changed {
			return nil
		}
		if _, err = tx.ExecContext(ctx, "UPDATE message_search_index SET body=? WHERE rowid=?", plain, id); err != nil {
			return err
		}
		if err = touchMessage(ctx, tx, id, room, now); err != nil {
			return err
		}
		touched = true
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	if staged != nil {
		staged.Keep()
	}
	d.PurgeDetached(purged)
	message, err := d.ReachableMessage(ctx, user, id)
	if err != nil {
		return message, err
	}
	if touched {
		d.noteTouch(message)
	}
	return message, nil
}
func (d *DB) DeleteMessage(ctx context.Context, user, id int64) error {
	return d.deleteMessage(ctx, user, id, true)
}
func (d *DB) RemoveBannedMessage(ctx context.Context, id int64) error {
	return d.deleteMessage(ctx, 0, id, false)
}
func (d *DB) deleteMessage(ctx context.Context, user, id int64, checkPermission bool) error {
	var blobs []int64
	var room int64
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var err error
		if checkPermission {
			room, err = messagePermission(ctx, tx, user, id, true)
		} else {
			err = tx.QueryRowContext(ctx, "SELECT room_id FROM messages WHERE id=?", id).Scan(&room)
		}
		if err != nil {
			return err
		}
		blobs, err = AttachmentBlobIDs(ctx, tx, "(record_type='Message' AND record_id=?) OR (record_type='ActionText::RichText' AND record_id IN (SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?))", id, id)
		if err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM boosts WHERE message_id=?",
			"DELETE FROM message_search_index WHERE rowid=?",
			"DELETE FROM active_storage_attachments WHERE record_type='ActionText::RichText' AND record_id IN (SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?)",
			"DELETE FROM active_storage_attachments WHERE record_type='Message' AND record_id=?",
			"DELETE FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?",
			"DELETE FROM messages WHERE id=?",
		} {
			if _, err = tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=?", Stamp(d.Now()), room)
		return err
	})
	if err == nil {
		d.PurgeDetached(blobs)
		d.noteDelete(room)
	}
	return err
}

type Boost struct {
	BoosterTitle             string
	BoosterUpdatedAt         time.Time
	ID, MessageID, BoosterID int64
	Content, Booster         string
	CreatedAt, UpdatedAt     time.Time
}

func (d *DB) Boosts(ctx context.Context, message int64) ([]Boost, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT b.id,b.message_id,b.booster_id,b.content,u.name,coalesce(u.bio,''),u.updated_at,b.created_at,b.updated_at FROM boosts b JOIN users u ON u.id=b.booster_id WHERE b.message_id=? ORDER BY b.created_at", message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Boost{}
	for rows.Next() {
		var b Boost
		var bio string
		if err = rows.Scan(&b.ID, &b.MessageID, &b.BoosterID, &b.Content, &b.Booster, &bio, timestamp{&b.BoosterUpdatedAt}, timestamp{&b.CreatedAt}, timestamp{&b.UpdatedAt}); err != nil {
			return nil, err
		}
		b.BoosterTitle = (User{Name: b.Booster, Bio: bio}).Title()
		result = append(result, b)
	}
	return result, rows.Err()
}
func (d *DB) CreateBoost(ctx context.Context, user, message int64, content string) (Boost, error) {
	now := d.Now()
	b := Boost{MessageID: message, BoosterID: user, Content: content, CreatedAt: now, UpdatedAt: now}
	var room int64
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var err error
		room, err = messagePermission(ctx, tx, user, message, false)
		if err != nil {
			return err
		}
		var bio string
		if err = tx.QueryRowContext(ctx, "SELECT name,coalesce(bio,''),updated_at FROM users WHERE id=?", user).Scan(&b.Booster, &bio, timestamp{&b.BoosterUpdatedAt}); err != nil {
			return err
		}
		b.BoosterTitle = (User{Name: b.Booster, Bio: bio}).Title()
		result, err := tx.ExecContext(ctx, "INSERT INTO boosts(message_id,booster_id,content,created_at,updated_at) VALUES (?,?,?,?,?)", message, user, content, Stamp(now), Stamp(now))
		if err != nil {
			return err
		}
		b.ID, err = result.LastInsertId()
		if err != nil {
			return err
		}
		return touchMessage(ctx, tx, message, room, Stamp(now))
	})
	if err == nil {
		d.noteTouch(Message{ID: message, RoomID: room, UpdatedAt: now})
	}
	return b, err
}
func (d *DB) DeleteBoost(ctx context.Context, user, message, id int64) error {
	now := d.Now()
	var room int64
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var err error
		room, err = messagePermission(ctx, tx, user, message, false)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM boosts WHERE id=? AND message_id=? AND booster_id=?", id, message, user)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return touchMessage(ctx, tx, message, room, Stamp(now))
	})
	if err == nil {
		d.noteTouch(Message{ID: message, RoomID: room, UpdatedAt: now})
	}
	return err
}

// Message is the unscoped model lookup used by background jobs.
func (d *DB) Message(ctx context.Context, id int64) (Message, error) {
	rows, err := d.Read.QueryContext(ctx, messageSelect+"WHERE m.id=?", id)
	if err != nil {
		return Message{}, err
	}
	messages, err := scanMessages(rows)
	if err != nil {
		return Message{}, err
	}
	if len(messages) == 0 {
		return Message{}, sql.ErrNoRows
	}
	return messages[0], nil
}
func (d *DB) FindRoom(ctx context.Context, id int64) (Room, error) {
	var room Room
	err := d.Read.QueryRowContext(ctx, "SELECT id,creator_id,coalesce(name,''),type,updated_at FROM rooms WHERE id=?", id).Scan(&room.ID, &room.CreatorID, &room.Name, &room.Type, timestamp{&room.UpdatedAt})
	return room, err
}

// MessagePageReferences leaves rich text and author loading to cache misses.
// Around/after pagination retains the same full-record path and ordering.
// The anchor-zero window (the room page and "after" with no cursor) is served
// from memory until a local commit or an observed external commit drops it.
// MessagePageReferences returns the content generation observed after the
// external sync and before the read. Callers that cache HTML must keep that
// value in the key and skip admission if ContentGeneration has moved.
func (d *DB) MessagePageReferences(ctx context.Context, room, anchor int64, direction string) ([]Message, uint64, error) {
	d.syncExternal(ctx)
	generation := d.generation()
	if direction != "before" && anchor != 0 {
		messages, err := d.MessagePage(ctx, room, anchor, direction)
		return messages, generation, err
	}
	if anchor == 0 {
		if messages, ok := d.cachedLatest(room); ok {
			d.countPage(true)
			return messages, generation, nil
		}
		d.countPage(false)
		epoch, seen := d.beginRoom(room)
		messages, err := d.queryMessageWindow(ctx, room, 0)
		if err != nil {
			return nil, generation, err
		}
		d.storeLatest(room, epoch, seen, messages)
		return messages, generation, nil
	}
	messages, err := d.queryMessageWindow(ctx, room, anchor)
	return messages, generation, err
}

func (d *DB) generation() uint64 {
	if s := d.state(); s != nil {
		return s.gen.Load()
	}
	return 0
}

func (d *DB) queryMessageWindow(ctx context.Context, room, anchor int64) ([]Message, error) {
	query := "SELECT id,updated_at FROM messages WHERE room_id=? "
	args := []any{room}
	if anchor != 0 {
		query += "AND created_at < (SELECT created_at FROM messages WHERE id=? AND room_id=?) "
		args = append(args, anchor, room)
	}
	rows, err := d.Read.QueryContext(ctx, query+"ORDER BY created_at DESC LIMIT 40", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		message := Message{RoomID: room}
		if err := rows.Scan(&message.ID, timestamp{&message.UpdatedAt}); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	slices.Reverse(messages)
	return messages, rows.Err()
}

func (d *DB) MessagesByID(ctx context.Context, ids []int64) ([]Message, error) {
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := d.Read.QueryContext(ctx, messageSelect+"WHERE m.id IN (SELECT value FROM json_each(?))", string(raw))
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}
