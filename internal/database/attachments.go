package database

import (
	"context"
	"database/sql"
)

// A nil assignment preserves absence; a zero ID detaches; Blob inserts metadata
// already prepared from staged bytes. No file handle or lifecycle enters SQL.
type AttachmentInput struct {
	ID   int64
	Blob *Blob
}

type AttachmentCommit struct {
	Uploaded *Blob
	Detached []int64
}

func (d *DB) assignAttachment(ctx context.Context, tx *sql.Tx, kind string, id int64, name string, input *AttachmentInput) (AttachmentCommit, error) {
	var commit AttachmentCommit
	if input == nil {
		return commit, nil
	}
	blobID := input.ID
	if input.Blob != nil {
		if blobID != 0 {
			return commit, ErrValidation
		}
		blob, err := d.insertBlob(ctx, tx, *input.Blob)
		if err != nil {
			return commit, err
		}
		commit.Uploaded = &blob
		blobID = blob.ID
	}
	var err error
	commit.Detached, err = AttachmentBlobIDs(ctx, tx, "record_type=? AND record_id=? AND name=?", kind, id, name)
	if err != nil {
		return AttachmentCommit{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type=? AND record_id=? AND name=?", kind, id, name); err != nil {
		return AttachmentCommit{}, err
	}
	if blobID != 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,?,?,?,?)", blobID, kind, id, name, Stamp(d.Now()))
	}
	return commit, err
}

func (d *DB) DeleteAvatar(ctx context.Context, actor int64) (AttachmentCommit, error) {
	return d.detachOwnedAttachment(ctx, actor, "User", "avatar")
}

func (d *DB) DeleteLogo(ctx context.Context, actor int64) (AttachmentCommit, error) {
	return d.detachOwnedAttachment(ctx, actor, "Account", "logo")
}

func (d *DB) detachOwnedAttachment(ctx context.Context, actor int64, kind, name string) (AttachmentCommit, error) {
	var commit AttachmentCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		id, table := actor, "users"
		if kind == "Account" {
			if err := administratorTx(ctx, tx, actor); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "SELECT id FROM accounts ORDER BY id LIMIT 1").Scan(&id); err != nil {
				return err
			}
			table = "accounts"
		} else {
			var active bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND status=0)", actor).Scan(&active); err != nil {
				return err
			}
			if !active {
				return ErrForbidden
			}
		}
		ids, err := AttachmentBlobIDs(ctx, tx, "record_type=? AND record_id=? AND name=?", kind, id, name)
		if err != nil || len(ids) == 0 {
			return err
		}
		commit, err = d.assignAttachment(ctx, tx, kind, id, name, &AttachmentInput{})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE "+table+" SET updated_at=? WHERE id=?", Stamp(d.Now()), id)
		return err
	})
	if err != nil {
		return AttachmentCommit{}, err
	}
	return commit, nil
}
