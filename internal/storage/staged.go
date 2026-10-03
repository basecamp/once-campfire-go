package storage

import (
	"context"
	"database/sql"
	"os"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// Staged owns an uploaded file until the record's transaction commits.
// No blob row exists until Insert is called inside that transaction.
type Staged struct {
	Blob  Blob
	path  string
	store *Store
}

func (s *Staged) Insert(ctx context.Context, tx *sql.Tx) (int64, error) {
	b := &s.Blob
	b.CreatedAt = database.Stamp(s.store.DB.Now())
	result, err := tx.ExecContext(ctx, "INSERT INTO active_storage_blobs(key,filename,content_type,metadata,service_name,byte_size,checksum,created_at) VALUES (?,?,?,?,?,?,?,?)", b.Key, b.Filename, b.ContentType, string(b.Metadata), b.ServiceName, b.ByteSize, b.Checksum, b.CreatedAt)
	if err != nil {
		return 0, err
	}
	b.ID, err = result.LastInsertId()
	return b.ID, err
}
func (s *Staged) Keep() { s.path = "" }
func (s *Staged) Discard() {
	if s.path != "" {
		os.Remove(s.path)
		s.path = ""
	}
}
func (s *Staged) Save(ctx context.Context) (Blob, error) {
	defer s.Discard()
	err := s.store.DB.Transaction(ctx, func(tx *sql.Tx) error { _, err := s.Insert(ctx, tx); return err })
	if err == nil {
		s.Keep()
	}
	return s.Blob, err
}
