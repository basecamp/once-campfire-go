package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Blob is an owned Active Storage record. Byte processing belongs to storage.
type Blob struct {
	ID          int64           `json:"id"`
	Key         string          `json:"key"`
	Filename    string          `json:"filename"`
	ContentType *string         `json:"content_type"`
	Metadata    json.RawMessage `json:"metadata"`
	ServiceName string          `json:"service_name"`
	ByteSize    int64           `json:"byte_size"`
	Checksum    string          `json:"checksum"`
	CreatedAt   string          `json:"created_at"`
}

func (b Blob) Type() string {
	if b.ContentType == nil {
		return ""
	}
	return *b.ContentType
}

const blobColumns = "b.id,b.key,b.filename,b.content_type,b.metadata,b.service_name,b.byte_size,b.checksum,b.created_at"

func scanBlob(row *sql.Row) (Blob, error) {
	var b Blob
	var metadata, checksum sql.NullString
	err := row.Scan(&b.ID, &b.Key, &b.Filename, &b.ContentType, &metadata, &b.ServiceName, &b.ByteSize, &checksum, &b.CreatedAt)
	b.Metadata = json.RawMessage(metadata.String)
	if !json.Valid(b.Metadata) {
		b.Metadata = json.RawMessage("{}")
	}
	b.Checksum = checksum.String
	return b, err
}

func (d *DB) Blob(ctx context.Context, id int64) (Blob, error) {
	return scanBlob(d.Read.QueryRowContext(ctx, "SELECT "+blobColumns+" FROM active_storage_blobs b WHERE b.id=?", id))
}

func (d *DB) AttachedBlob(ctx context.Context, kind string, id int64, name string) (Blob, error) {
	return scanBlob(d.Read.QueryRowContext(ctx, "SELECT "+blobColumns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id WHERE a.record_type=? AND a.record_id=? AND a.name=? ORDER BY a.id LIMIT 1", kind, id, name))
}

func (d *DB) VariantBlob(ctx context.Context, source int64, digest string) (Blob, error) {
	return variantBlob(ctx, d.Read, source, digest)
}

// A reader and a transaction expose the same row boundary without exporting pools.
type blobReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func variantBlob(ctx context.Context, reader blobReader, source int64, digest string) (Blob, error) {
	return scanBlob(reader.QueryRowContext(ctx, "SELECT "+blobColumns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id JOIN active_storage_variant_records v ON v.id=a.record_id WHERE a.record_type='ActiveStorage::VariantRecord' AND a.name='image' AND v.blob_id=? AND v.variation_digest=? LIMIT 1", source, digest))
}

func (d *DB) insertBlob(ctx context.Context, tx *sql.Tx, b Blob) (Blob, error) {
	b.ID = 0
	if b.Key == "" {
		b.Key = rails.StorageKey()
	}
	if b.ServiceName == "" {
		b.ServiceName = "local"
	}
	if len(b.Metadata) == 0 {
		b.Metadata = json.RawMessage("{}")
	}
	b.CreatedAt = Stamp(d.Now())
	result, err := tx.ExecContext(ctx, "INSERT INTO active_storage_blobs(key,filename,content_type,metadata,service_name,byte_size,checksum,created_at) VALUES (?,?,?,?,?,?,?,?)", b.Key, b.Filename, b.ContentType, string(b.Metadata), b.ServiceName, b.ByteSize, b.Checksum, b.CreatedAt)
	if err != nil {
		return Blob{}, err
	}
	b.ID, err = result.LastInsertId()
	return b, err
}

func (d *DB) CreateBlob(ctx context.Context, input Blob) (Blob, error) {
	var b Blob
	err := d.Transaction(ctx, func(tx *sql.Tx) error { var err error; b, err = d.insertBlob(ctx, tx, input); return err })
	if err != nil {
		return Blob{}, err
	}
	return b, nil
}

func (d *DB) UpdateBlobMetadata(ctx context.Context, id int64, metadata json.RawMessage) error {
	return d.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE active_storage_blobs SET metadata=? WHERE id=?", string(metadata), id); err != nil {
			return err
		}
		now := Stamp(d.Now())
		for _, record := range []struct{ kind, table string }{{"Message", "messages"}, {"User", "users"}, {"Account", "accounts"}} {
			if _, err := tx.ExecContext(ctx, "UPDATE "+record.table+" SET updated_at=? WHERE id IN (SELECT record_id FROM active_storage_attachments WHERE blob_id=? AND record_type=?)", now, id, record.kind); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id IN (SELECT room_id FROM messages WHERE id IN (SELECT record_id FROM active_storage_attachments WHERE blob_id=? AND record_type='Message'))", now, id)
		return err
	})
}

// DerivativeCommit captures the winner in the same transaction as linking it.
// Created transfers the caller's staged bytes; losers must discard their bytes.
type DerivativeCommit struct {
	Blob    Blob
	Created bool
}

func (d *DB) LinkVariant(ctx context.Context, source int64, digest string, input Blob) (DerivativeCommit, error) {
	var commit DerivativeCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT INTO active_storage_variant_records(blob_id,variation_digest) VALUES (?,?) ON CONFLICT(blob_id,variation_digest) DO NOTHING", source, digest)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			commit.Blob, err = variantBlob(ctx, tx, source, digest)
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		commit.Blob, err = d.insertBlob(ctx, tx, input)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'ActiveStorage::VariantRecord',?,'image',?)", commit.Blob.ID, id, Stamp(d.Now()))
		commit.Created = err == nil
		return err
	})
	if err != nil {
		return DerivativeCommit{}, err
	}
	return commit, nil
}

func (d *DB) LinkPreview(ctx context.Context, source int64, input Blob) (DerivativeCommit, error) {
	var commit DerivativeCommit
	err := d.Transaction(ctx, func(tx *sql.Tx) error {
		var err error
		commit.Blob, err = scanBlob(tx.QueryRowContext(ctx, "SELECT "+blobColumns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id WHERE a.record_type='ActiveStorage::Blob' AND a.record_id=? AND a.name='preview_image' ORDER BY a.id LIMIT 1", source))
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var sourceID int64
		if err = tx.QueryRowContext(ctx, "SELECT id FROM active_storage_blobs WHERE id=?", source).Scan(&sourceID); err != nil {
			return err
		}
		commit.Blob, err = d.insertBlob(ctx, tx, input)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'ActiveStorage::Blob',?,'preview_image',?)", commit.Blob.ID, sourceID, Stamp(d.Now()))
		commit.Created = err == nil
		return err
	})
	if err != nil {
		return DerivativeCommit{}, err
	}
	return commit, nil
}
