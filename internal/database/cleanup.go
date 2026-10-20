package database

import (
	"context"
	"database/sql"
)

// AttachmentBlobIDs is called inside the transaction that removes the attachment
// rows; its result is dispatched only after that transaction commits.
func AttachmentBlobIDs(ctx context.Context, tx *sql.Tx, condition string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT blob_id FROM active_storage_attachments WHERE "+condition, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (d *DB) MessagesByCreator(ctx context.Context, user int64) ([]Message, error) {
	rows, err := d.Read.QueryContext(ctx, messageSelect+"WHERE m.creator_id=? ORDER BY m.id", user)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}
