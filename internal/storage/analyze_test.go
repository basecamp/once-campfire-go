package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestMediaMetadataAndTrackedPreview(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "db.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, _ := rails.NewSecrets("media-test")
	store := New(db, secrets, root)
	var vectors struct {
		Messages []struct {
			Fixture  string
			Declared string `json:"declared_type"`
			Blob     struct{ Metadata string }
		}
	}
	raw, err := os.ReadFile("../../reference/vectors/storage.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors.Messages {
		t.Run(v.Fixture, func(t *testing.T) {
			file, err := os.Open(filepath.Join("../../reference/reference/test/fixtures/files", v.Fixture))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			blob, err := store.Stage(ctx, v.Fixture, v.Declared, file)
			if err != nil {
				t.Fatal(err)
			}
			blob, err = store.Analyze(ctx, blob)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			json.Unmarshal([]byte(v.Blob.Metadata), &want)
			json.Unmarshal(blob.Metadata, &got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("metadata got %s want %s", blob.Metadata, v.Blob.Metadata)
			}
			if Previewable(blob.Type()) {
				var images [2]Blob
				var errs [2]error
				var wg sync.WaitGroup
				for i := range images {
					wg.Add(1)
					go func(i int) { defer wg.Done(); images[i], errs[i] = store.PreviewImage(ctx, blob) }(i)
				}
				wg.Wait()
				for _, err := range errs {
					if err != nil {
						t.Fatal(err)
					}
				}
				if images[0].ID != images[1].ID {
					t.Fatal("duplicate preview")
				}
				preview, err := store.Representation(ctx, blob, Variation{{Key: "format", Value: Symbol("webp")}})
				if err != nil {
					t.Fatal(err)
				}
				if preview.Type() != "image/webp" {
					t.Fatal(preview.Type())
				}
			}
		})
	}
}
func TestRotatedVideoMetadata(t *testing.T) {
	var probe map[string]any
	json.Unmarshal([]byte(`{"streams":[{"codec_type":"video","width":1920,"height":1080,"side_data_list":[{"side_data_type":"Display Matrix","rotation":-90}]},{"codec_type":"audio"}],"format":{"duration":"3.5"}}`), &probe)
	got, err := mediaMetadata(probe, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["width"] != json.Number("1080.0") || got["height"] != json.Number("1920.0") || got["duration"] != json.Number("3.5") || got["audio"] != true {
		t.Fatal(got)
	}
}
