package database

import (
	"context"
	"database/sql"
	"fmt"
)

// recordWithUpload commits the record and its avatar/logo together. Files staged
// before the transaction are removed on failure; old blobs are purged after commit.
func (d *DB) recordWithUpload(ctx context.Context, kind string, id *int64, uploads []BlobStager, update func(*sql.Tx) error) error {
	var staged BlobStager
	if len(uploads) > 0 {
		staged = uploads[0]
	}
	if staged == nil {
		return d.Transaction(ctx, update)
	}
	defer staged.Discard()
	name := "avatar"
	if kind == "Account" {
		name = "logo"
	} else if kind != "User" {
		return fmt.Errorf("invalid upload record type %q", kind)
	}
	var purged []int64
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		if err := update(tx); err != nil {
			return err
		}
		blob, err := staged.Insert(ctx, tx)
		if err != nil {
			return err
		}
		purged, err = AttachmentBlobIDs(ctx, tx, "record_type=? AND record_id=? AND name=?", kind, *id, name)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type=? AND record_id=? AND name=?", kind, *id, name); err != nil {
			return err
		}
		if blob == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,?,?,?,?)", blob, kind, *id, name, Stamp(d.Now()))
		return err
	})
	if err != nil {
		return err
	}
	staged.Keep()
	d.PurgeDetached(purged)
	return nil
}
