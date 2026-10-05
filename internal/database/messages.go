package database

import (
	"context"
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
		return Message{}, ErrNoRows
	}
	return messages[0], nil
}

func messagePermission(ctx context.Context, tx *Tx, user, id int64, administer bool) (room int64, err error) {
	var creator int64
	var role int
	err = tx.QueryRowContext(ctx, "SELECT m.room_id,m.creator_id,u.role FROM messages m JOIN memberships member ON member.room_id=m.room_id JOIN users u ON u.id=member.user_id WHERE m.id=? AND u.id=? AND u.status=0", id, user).Scan(&room, &creator, &role)
	if err == nil && administer && creator != user && role != 1 {
		err = ErrForbidden
	}
	return
}
func touchMessage(ctx context.Context, tx *Tx, id, room int64, now string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE messages SET updated_at=? WHERE id=?", now, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=?", now, room)
	return err
}

// UpdateMessageWithUpload updates a message: a nil body or attachment leaves that attribute
// unchanged; attachment zero removes it. Body, attachment, timestamps and the search index
// commit together.
func (d *DB) UpdateMessageWithUpload(ctx context.Context, user, id int64, body *string, plain string, attachment *int64, staged BlobStager) (Message, error) {
	if staged != nil {
		defer staged.Discard()
	}
	var purged []int64
	err := d.Transaction(ctx, func(tx *Tx) error {
		room, err := messagePermission(ctx, tx, user, id, true)
		if err != nil {
			return err
		}
		now := Stamp(d.Now())
		changed := false
		if body != nil {
			var old NullString
			err = tx.QueryRowContext(ctx, "SELECT body FROM action_text_rich_texts WHERE record_type='Message' AND record_id=? AND name='body'", id).Scan(&old)
			if err != nil && err != ErrNoRows {
				return err
			}
			if err == ErrNoRows {
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
		return touchMessage(ctx, tx, id, room, now)
	})
	if err != nil {
		return Message{}, err
	}
	if staged != nil {
		staged.Keep()
	}
	d.PurgeDetached(purged)
	return d.ReachableMessage(ctx, user, id)
}
func (d *DB) DeleteMessage(ctx context.Context, user, id int64) error {
	return d.deleteMessage(ctx, user, id, true)
}
func (d *DB) RemoveBannedMessage(ctx context.Context, id int64) error {
	return d.deleteMessage(ctx, 0, id, false)
}
func (d *DB) deleteMessage(ctx context.Context, user, id int64, checkPermission bool) error {
	var blobs []int64
	err := d.Transaction(ctx, func(tx *Tx) error {
		var room int64
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

func (d *DB) CreateBoost(ctx context.Context, user, message int64, content string) (Boost, error) {
	now := d.Now()
	b := Boost{MessageID: message, BoosterID: user, Content: content, CreatedAt: now, UpdatedAt: now}
	err := d.Transaction(ctx, func(tx *Tx) error {
		room, err := messagePermission(ctx, tx, user, message, false)
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
	return b, err
}
func (d *DB) DeleteBoost(ctx context.Context, user, message, id int64) error {
	return d.Transaction(ctx, func(tx *Tx) error {
		room, err := messagePermission(ctx, tx, user, message, false)
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
			return ErrNoRows
		}
		return touchMessage(ctx, tx, message, room, Stamp(d.Now()))
	})
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
		return Message{}, ErrNoRows
	}
	return messages[0], nil
}
func (d *DB) FindRoom(ctx context.Context, id int64) (Room, error) {
	var room Room
	err := d.Read.QueryRowContext(ctx, "SELECT id,creator_id,coalesce(name,''),type,updated_at FROM rooms WHERE id=?", id).Scan(&room.ID, &room.CreatorID, &room.Name, &room.Type, timestamp{&room.UpdatedAt})
	return room, err
}
