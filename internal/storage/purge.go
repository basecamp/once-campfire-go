package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Purge refuses attached blobs and removes tracked variants and preview images
// before their files, matching ActiveStorage::Blob#purge.
func (s *Store) Purge(ctx context.Context, id int64) error {
	pending := []int64{id}
	seen := map[int64]bool{}
	for len(pending) > 0 {
		id = pending[0]
		pending = pending[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		var key string
		var dependent []int64
		removed := false
		err := s.DB.Transaction(ctx, func(tx *sql.Tx) error {
			err := tx.QueryRowContext(ctx, "SELECT key FROM active_storage_blobs WHERE id=?", id).Scan(&key)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM active_storage_attachments WHERE blob_id=?", id).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return nil
			}
			condition := "(record_type='ActiveStorage::VariantRecord' AND record_id IN (SELECT id FROM active_storage_variant_records WHERE blob_id=?)) OR (record_type='ActiveStorage::Blob' AND record_id=? AND name='preview_image')"
			dependent, err = database.AttachmentBlobIDs(ctx, tx, condition, id, id)
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
			removed = true
			return nil
		})
		if err != nil {
			return err
		}
		if !removed {
			continue
		}
		path, err := s.Path(key)
		if err != nil {
			return err
		}
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = os.RemoveAll(filepath.Join(s.Root, "variants", key)); err != nil {
			return err
		}
		pending = append(pending, dependent...)
	}
	return nil
}
