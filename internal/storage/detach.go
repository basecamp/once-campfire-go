package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func attachmentTable(kind string) string {
	switch kind {
	case "User":
		return "users"
	case "Account":
		return "accounts"
	case "Message":
		return "messages"
	}
	return ""
}

func (s *Store) Detach(ctx context.Context, kind string, id int64, name string) error {
	table := attachmentTable(kind)
	if table == "" {
		return fmt.Errorf("unsupported attachment record %q", kind)
	}
	var blobs []int64
	err := s.DB.Transaction(ctx, func(tx *sql.Tx) error {
		var err error
		blobs, err = database.AttachmentBlobIDs(ctx, tx, "record_type=? AND record_id=? AND name=?", kind, id, name)
		if err != nil || len(blobs) == 0 {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type=? AND record_id=? AND name=?", kind, id, name); err != nil {
			return err
		}
		now := database.Stamp(s.DB.Now())
		if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET updated_at=? WHERE id=?", now, id); err != nil {
			return err
		}
		if kind == "Message" {
			_, err = tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id=(SELECT room_id FROM messages WHERE id=?)", now, id)
		}
		return err
	})
	if err == nil {
		s.DB.PurgeDetached(blobs)
	}
	return err
}
