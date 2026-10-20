package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestPurgeRetainsFrontierAndFileIdentityAfterUnlinkFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "db.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, err := rails.NewSecrets("purge-test")
	if err != nil {
		t.Fatal(err)
	}
	store := New(db, secrets, root)
	stage := func(name string) database.Blob {
		t.Helper()
		b, err := store.Stage(ctx, name, "text/plain", strings.NewReader(name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	source, variant, shared := stage("source"), stage("variant"), stage("shared-preview")
	err = db.Transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT INTO active_storage_variant_records(blob_id,variation_digest) VALUES (?, 'test')", source.ID)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES
			(?,'ActiveStorage::VariantRecord',?,'image',?),
			(?,'ActiveStorage::Blob',?,'preview_image',?),
			(?,'User',1,'avatar',?)`, variant.ID, id, database.Stamp(db.Now()), shared.ID, source.ID, database.Stamp(db.Now()), shared.ID, database.Stamp(db.Now()))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Path(source.Key)
	if err != nil {
		t.Fatal(err)
	}
	backup := path + ".original"
	if err = os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(path, "obstruction")
	if err = os.WriteFile(obstruction, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(store.Root, "variants", source.Key)
	if err = os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(legacy, "untracked"), []byte("variant"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err = store.Purge(ctx, source.ID); err == nil {
		t.Fatal("unlink obstruction was not reported")
	}
	for _, b := range []database.Blob{source, variant} {
		if _, err = store.DB.Blob(ctx, b.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("removed blob %s remains: %v", b.Filename, err)
		}
	}
	variantPath, _ := store.Path(variant.Key)
	if _, err = os.Stat(variantPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("independent variant was not unlinked: %v", err)
	}
	if _, err = store.DB.Blob(ctx, shared.ID); err != nil {
		t.Fatalf("shared preview was removed: %v", err)
	}
	sharedPath, _ := store.Path(shared.Key)
	if _, err = os.Stat(sharedPath); err != nil {
		t.Fatalf("shared preview file was removed: %v", err)
	}

	if err = os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	// The original row no longer exists. Retrying its ID must still own its key.
	retries := make(chan error, 4)
	for range 4 {
		go func() { retries <- store.Purge(ctx, source.ID) }()
	}
	for range 4 {
		if err = <-retries; err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{path, legacy} {
		if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retry lost removed file identity %s: %v", path, err)
		}
	}
}
