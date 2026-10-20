package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"uuid"

	"github.com/basecamp/once-campfire-go/internal/richtext"
)

// MessageInput preserves absent body/attachment attributes. Search text is never
// supplied by a caller: it is derived from the state committed by this command.
type MessageInput struct {
	ClientID   string
	Body       *string
	Attachment *int64
	Upload     *Blob
}

// MessageCommit contains the record and room observed by the successful mutation.
// Detached blobs are post-commit obligations, not database callbacks.
type MessageCommit struct {
	Message
	Room         Room
	Detached     []int64
	AttachmentID int64
	Uploaded     *Blob
}

func (d *DB) CreateMessage(ctx context.Context, user, room int64, input MessageInput) (MessageCommit, error) {
	return d.createMessage(ctx, user, room, input, true)
}

// CreateWebhookReply is only for an already-authorized queued delivery. Like the
// reference model callback, it does not reapply controller membership checks.
func (d *DB) CreateWebhookReply(ctx context.Context, user, room int64, input MessageInput) (MessageCommit, error) {
	return d.createMessage(ctx, user, room, input, false)
}

func (d *DB) createMessage(ctx context.Context, user, room int64, input MessageInput, checkMembership bool) (MessageCommit, error) {
	if input.ClientID == "" {
		input.ClientID = uuid.NewV4().String()
	}
	if input.Body != nil {
		body := richtext.Canonical(*input.Body)
		input.Body = &body
	}
	now := d.Now().UTC()
	result := MessageCommit{Message: Message{RoomID: room, CreatorID: user, ClientID: input.ClientID, CreatedAt: now, UpdatedAt: now}}
	if input.Body != nil {
		result.Body = *input.Body
	}
	search := prepareMessageSearchBody(result.Body)
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		// Select permission and receipt inputs together on the writer. Creation
		// overwrites the room timestamp, so its previous value is not an input.
		query := "SELECT r.id,r.creator_id,coalesce(r.name,''),r.type,u.name FROM rooms r JOIN users u ON u.id=? WHERE r.id=?"
		if checkMembership {
			query += " AND u.status=0 AND EXISTS(SELECT 1 FROM memberships m WHERE m.room_id=r.id AND m.user_id=u.id)"
		}
		err := tx.QueryRowContext(ctx, query, user, room).Scan(&result.Room.ID, &result.Room.CreatorID, &result.Room.Name, &result.Room.Type, &result.Creator)
		if checkMembership && errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		blob, uploaded, err := d.messageUpload(ctx, tx, input)
		if err != nil {
			return err
		}
		result.AttachmentID = blob
		result.Uploaded = uploaded
		stamp := Stamp(now)
		r, err := tx.ExecContext(ctx, "INSERT INTO messages(client_message_id,creator_id,room_id,created_at,updated_at) VALUES (?,?,?,?,?)", result.ClientID, user, room, stamp, stamp)
		if err != nil {
			return err
		}
		result.ID, err = r.LastInsertId()
		if err != nil {
			return err
		}
		if input.Body != nil {
			if _, err = tx.ExecContext(ctx, "INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)", result.ID, result.Body, stamp, stamp); err != nil {
				return err
			}
		}
		if blob != 0 {
			if _, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'Message',?,'attachment',?)", blob, result.ID, stamp); err != nil {
				return err
			}
		}
		plain, err := messageSearchText(ctx, tx, result.ID, search)
		if err != nil {
			return err
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO message_search_index(rowid,body) VALUES (?,?)", []any{result.ID, plain}},
			{"UPDATE rooms SET updated_at=? WHERE id=?", []any{stamp, room}},
			{"UPDATE memberships SET unread_at=?,updated_at=? WHERE room_id=? AND user_id!=? AND involvement!='invisible' AND (connected_at IS NULL OR connected_at < ?)", []any{stamp, stamp, room, user, Stamp(now.Add(-60 * time.Second))}},
		} {
			if _, err = tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		result.Room.UpdatedAt = now
		return nil
	})
	if err != nil {
		return MessageCommit{}, err
	}
	return result, nil
}

func (d *DB) UpdateMessage(ctx context.Context, user, id int64, input MessageInput) (MessageCommit, error) {
	var search messageSearchBody
	if input.Body != nil {
		body := richtext.Canonical(*input.Body)
		input.Body = &body
		search = prepareMessageSearchBody(body)
	}
	var result MessageCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		room, err := messagePermission(ctx, tx, user, id)
		if err != nil {
			return err
		}
		m := &result.Message
		if err = tx.QueryRowContext(ctx, messageSelect+"WHERE m.id=?", id).Scan(&m.ID, &m.RoomID, &m.CreatorID, &m.ClientID, &m.Body, &m.Creator, timestamp{&m.CreatedAt}, timestamp{&m.UpdatedAt}); err != nil {
			return err
		}
		if err = commandRoom(ctx, tx, room, &result.Room); err != nil {
			return err
		}
		now := d.Now().UTC()
		stamp := Stamp(now)
		changed := false
		if input.Body != nil {
			var old sql.NullString
			err = tx.QueryRowContext(ctx, "SELECT body FROM action_text_rich_texts WHERE record_type='Message' AND record_id=? AND name='body'", id).Scan(&old)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if errors.Is(err, sql.ErrNoRows) {
				_, err = tx.ExecContext(ctx, "INSERT INTO action_text_rich_texts(name,record_type,record_id,body,created_at,updated_at) VALUES ('body','Message',?,?,?,?)", id, *input.Body, stamp, stamp)
				changed = true
			} else if !old.Valid || old.String != *input.Body {
				_, err = tx.ExecContext(ctx, "UPDATE action_text_rich_texts SET body=?,updated_at=? WHERE record_type='Message' AND record_id=? AND name='body'", *input.Body, stamp, id)
				changed = true
			}
			if err != nil {
				return err
			}
			m.Body = *input.Body
		}
		attachment := input.Attachment
		if input.Upload != nil {
			blob, err := d.insertBlob(ctx, tx, *input.Upload)
			if err != nil {
				return err
			}
			result.Uploaded = &blob
			attachment = &blob.ID
		}
		if attachment != nil {
			result.Detached, err = AttachmentBlobIDs(ctx, tx, "record_type='Message' AND record_id=? AND name='attachment'", id)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type='Message' AND record_id=? AND name='attachment'", id); err != nil {
				return err
			}
			if *attachment != 0 {
				if _, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'Message',?,'attachment',?)", *attachment, id, stamp); err != nil {
					return err
				}
			}
			result.AttachmentID = *attachment
			changed = changed || len(result.Detached) > 0 || *attachment != 0
		}
		if !changed {
			return nil
		}
		if input.Body == nil {
			// An omitted body comes from the writer's current record, not from
			// earlier caller preparation.
			search = prepareMessageSearchBody(m.Body)
		}
		plain, err := messageSearchText(ctx, tx, m.ID, search)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE message_search_index SET body=? WHERE rowid=?", plain, id); err != nil {
			return err
		}
		if err = touchMessage(ctx, tx, id, room, stamp); err != nil {
			return err
		}
		m.UpdatedAt = now
		result.Room.UpdatedAt = now
		return nil
	})
	if err != nil {
		return MessageCommit{}, err
	}
	return result, nil
}

func (d *DB) DeleteMessage(ctx context.Context, user, id int64) (MessageCommit, error) {
	return d.deleteMessage(ctx, user, id, true)
}

func (d *DB) RemoveBannedMessage(ctx context.Context, id int64) (MessageCommit, error) {
	return d.deleteMessage(ctx, 0, id, false)
}

func (d *DB) deleteMessage(ctx context.Context, user, id int64, checkPermission bool) (MessageCommit, error) {
	var result MessageCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if checkPermission {
			if _, err := messagePermission(ctx, tx, user, id); err != nil {
				return err
			}
		}
		m := &result.Message
		if err := tx.QueryRowContext(ctx, messageSelect+"WHERE m.id=?", id).Scan(&m.ID, &m.RoomID, &m.CreatorID, &m.ClientID, &m.Body, &m.Creator, timestamp{&m.CreatedAt}, timestamp{&m.UpdatedAt}); err != nil {
			return err
		}
		if err := commandRoom(ctx, tx, m.RoomID, &result.Room); err != nil {
			return err
		}
		var err error
		result.Detached, err = AttachmentBlobIDs(ctx, tx, "(record_type='Message' AND record_id=?) OR (record_type='ActionText::RichText' AND record_id IN (SELECT id FROM action_text_rich_texts WHERE record_type='Message' AND record_id=?))", id, id)
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
		result.Room.UpdatedAt = d.Now().UTC()
		_, err = tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=?", Stamp(result.Room.UpdatedAt), m.RoomID)
		return err
	})
	if err != nil {
		return MessageCommit{}, err
	}
	return result, nil
}

func (d *DB) messageUpload(ctx context.Context, tx *sql.Tx, input MessageInput) (int64, *Blob, error) {
	if input.Upload != nil {
		blob, err := d.insertBlob(ctx, tx, *input.Upload)
		if err != nil {
			return 0, nil, err
		}
		return blob.ID, &blob, nil
	}
	if input.Attachment != nil {
		return *input.Attachment, nil, nil
	}
	return 0, nil, nil
}
