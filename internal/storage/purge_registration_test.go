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

// Exercise retirement at the reference-check boundary deterministically: a live
// incoming reference makes an old traversal complete, then the final detach must
// retain a new obligation even if that traversal has not yet retired its root.
func TestPurgeRetirementPreservesNewerDetach(t *testing.T) {
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
	blob, err := store.Stage(ctx, "shared.txt", "text/plain", strings.NewReader("shared"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Write.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'User',1,'avatar',?)", blob.ID, database.Stamp(db.Now())); err != nil {
		t.Fatal(err)
	}

	p := &store.purges
	work := p.register(blob.ID)
	if err = p.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	registration := work.registration // no concurrent caller in this controlled case
	receipt, err := db.RemoveBlob(ctx, blob.ID)
	if err != nil || receipt.ID != 0 {
		t.Fatal(receipt, err)
	}
	// This is Purge's handling of the referenced candidate, before retirement.
	delete(work.candidates, blob.ID)
	if _, err = db.Write.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE blob_id=?", blob.ID); err != nil {
		t.Fatal(err)
	}
	store.RetainPurges([]int64{blob.ID})
	p.complete(blob.ID, work, registration)
	<-p.gate

	// No second task runs. The retained root alone must let shutdown resume it.
	if err = store.RetryPurges(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Blob(ctx, blob.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new detach lost: %v", err)
	}
	path, err := store.Path(blob.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new detach file lost: %v", err)
	}
}
