package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/mattn/go-sqlite3"
)

// Cancel at the real SQLite linking boundary, after generation and analysis.
// Both the generated file and the blob must remain owned by the transaction.
func TestCancelledDerivativeLinkLeavesNoOrphans(t *testing.T) {
	for _, fixture := range []string{"moon.jpg", "alpha-centuri.mov"} {
		t.Run(fixture, func(t *testing.T) {
			root := t.TempDir()
			db, err := database.Open(filepath.Join(root, "db.sqlite3"), 2)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			secrets, err := rails.NewSecrets("cancel-derivative")
			if err != nil {
				t.Fatal(err)
			}
			store := New(db, secrets, root)
			source, err := os.Open(
				filepath.Join("../../reference/reference/test/fixtures/files", fixture),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			ct := "image/jpeg"
			if fixture == "alpha-centuri.mov" {
				ct = "video/quicktime"
			}
			blob, err := store.Stage(context.Background(), fixture, ct, source)
			if err != nil {
				t.Fatal(err)
			}
			before := storedFiles(t, store.Root)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			connection, err := db.Write.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = connection.Raw(func(raw any) error {
				return raw.(*sqlite3.SQLiteConn).RegisterFunc("cancel_derivative", func() int {
					cancel()
					return 0
				}, false)
			})
			if err == nil {
				_, err = connection.ExecContext(context.Background(), `CREATE TRIGGER cancel_link
					BEFORE INSERT ON active_storage_attachments
					WHEN NEW.record_type IN ('ActiveStorage::VariantRecord', 'ActiveStorage::Blob')
					BEGIN SELECT cancel_derivative(); SELECT RAISE(ABORT, 'cancelled link'); END`)
			}
			connection.Close()
			if err != nil {
				t.Fatal(err)
			}
			if fixture == "moon.jpg" {
				_, err = store.Variant(ctx, blob, Resize(128, 128, "webp"))
			} else {
				_, err = store.PreviewImage(ctx, blob)
			}
			if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("link did not cancel: %v, context %v", err, ctx.Err())
			}
			for table, want := range map[string]int{"active_storage_blobs": 1, "active_storage_attachments": 0, "active_storage_variant_records": 0} {
				var count int
				if err := db.Read.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil ||
					count != want {
					t.Errorf("%s: got %d, want %d: %v", table, count, want, err)
				}
			}
			if after := storedFiles(t, store.Root); !reflect.DeepEqual(after, before) {
				t.Errorf("cancelled derivative retained files: before %v, after %v", before, after)
			}
		})
	}
}

func storedFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
