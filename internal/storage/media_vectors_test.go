//go:build media_vectors

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Use the pinned media toolchain, including FFmpeg's architecture. A fresh Rails
// capture can be selected explicitly, as in Rust; the checked-in vectors remain
// the default and no version or byte comparison is skipped.
func TestMediaOutputBytes(t *testing.T) {
	type vectorBlob struct {
		database.Blob
		Metadata string `json:"metadata"`
	}
	type output struct {
		File            string
		Blob            vectorBlob
		Transformations json.RawMessage `json:"transformations_typed"`
	}
	type fixture struct {
		Fixture  string
		Blob     vectorBlob
		Variants []output
		Preview  *output `json:"preview_image"`
	}
	var data struct {
		Messages, Avatars, Logos []fixture
		Versions                 map[string]string
	}
	vectors := os.Getenv("CAMPFIRE_STORAGE_VECTORS")
	if vectors == "" {
		vectors = "../../reference/vectors/storage.json"
	}
	raw, err := os.ReadFile(vectors)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if got := vipsVersion(); got != data.Versions["libvips"] {
		t.Fatalf("libvips %s; vectors require %s", got, data.Versions["libvips"])
	}
	ffmpeg, err := exec.Command("ffmpeg", "-version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(ffmpeg), data.Versions["ffmpeg"]) {
		t.Fatal("ffmpeg version differs from vectors")
	}
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "db.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, _ := rails.NewSecrets("media-vectors")
	store := New(db, secrets, root)
	ctx := context.Background()
	check := func(t *testing.T, blob database.Blob, want vectorBlob, expectedFile string) {
		t.Helper()
		path, err := store.Path(blob.Key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(expectedFile)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf(
				"bytes differ, size %d/%d checksum %s/%s",
				len(got),
				len(expected),
				blob.Checksum,
				want.Checksum,
			)
		}
		if blob.Type() != want.Type() || blob.Filename != want.Filename ||
			blob.ServiceName != want.ServiceName || blob.ByteSize != want.ByteSize ||
			blob.Checksum != want.Checksum {
			t.Errorf("blob got %+v; want %+v", blob, want)
		}
		var gotMetadata, wantMetadata any
		if err := json.Unmarshal(blob.Metadata, &gotMetadata); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want.Metadata), &wantMetadata); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotMetadata, wantMetadata) {
			t.Errorf("metadata got %s; want %s", blob.Metadata, want.Metadata)
		}
	}
	for _, fixtures := range [][]fixture{data.Messages, data.Avatars, data.Logos} {
		for _, v := range fixtures {
			t.Run(v.Fixture, func(t *testing.T) {
				original := filepath.Join(
					"../../reference/reference/test/fixtures/files",
					v.Fixture,
				)
				file, err := os.Open(original)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				blob, err := store.Stage(ctx, v.Fixture, v.Blob.Type(), file)
				if err != nil {
					t.Fatal(err)
				}
				blob, err = store.Analyze(ctx, blob)
				if err != nil {
					t.Fatal(err)
				}
				t.Run("original", func(t *testing.T) { check(t, blob, v.Blob, original) })
				if v.Preview != nil {
					t.Run(v.Preview.File, func(t *testing.T) {
						preview, err := store.PreviewImage(ctx, blob)
						if err != nil {
							t.Fatal(err)
						}
						check(
							t,
							preview,
							v.Preview.Blob,
							filepath.Join(filepath.Dir(vectors), "storage", v.Preview.File),
						)
					})
				}
				for _, variant := range v.Variants {
					t.Run(variant.File, func(t *testing.T) {
						result, err := store.Representation(
							ctx,
							blob,
							typedValue(t, variant.Transformations).(Variation),
						)
						if err != nil {
							t.Fatal(err)
						}
						check(
							t,
							result,
							variant.Blob,
							filepath.Join(filepath.Dir(vectors), "storage", variant.File),
						)
					})
				}
			})
		}
	}
}
