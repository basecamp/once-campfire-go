package database

import (
	"context"
	"database/sql"
	"errors"
)

// BlobRemoval transfers file identity and descendant candidates out of the
// transaction. Missing or currently attached blobs have no removal receipt.
type BlobRemoval struct {
	ID          int64
	Key         string
	Descendants []int64
}

func (d *DB) RemoveBlob(ctx context.Context, id int64) (BlobRemoval, error) {
	var removed BlobRemoval
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var key string
		err := tx.QueryRowContext(ctx, "SELECT key FROM active_storage_blobs WHERE id=?", id).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var attached bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM active_storage_attachments WHERE blob_id=?)", id).Scan(&attached); err != nil {
			return err
		}
		if attached {
			return nil
		}
		condition := "(record_type='ActiveStorage::VariantRecord' AND record_id IN (SELECT id FROM active_storage_variant_records WHERE blob_id=?)) OR (record_type='ActiveStorage::Blob' AND record_id=? AND name='preview_image')"
		descendants, err := AttachmentBlobIDs(ctx, tx, condition, id, id)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE "+condition, id, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_variant_records WHERE blob_id=?", id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_blobs WHERE id=?", id); err != nil {
			return err
		}
		removed = BlobRemoval{ID: id, Key: key, Descendants: descendants}
		return nil
	})
	if err != nil {
		return BlobRemoval{}, err
	}
	return removed, nil
}
