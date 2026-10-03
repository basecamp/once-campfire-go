//go:build media_vectors

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// Run in the Docker toolchain target to use the pinned media libraries.
func TestMediaOutputBytes(t *testing.T) {
	type output struct {
		File            string
		Blob            Blob
		Transformations json.RawMessage `json:"transformations_typed"`
	}
	type fixture struct {
		Fixture  string
		Blob     Blob
		Variants []output
		Preview  *output `json:"preview_image"`
	}
	var data struct {
		Messages, Avatars, Logos []fixture
		Versions                 map[string]string
	}
	raw, err := os.ReadFile("../../reference/vectors/storage.json")
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
	check := func(t *testing.T, blob Blob, want output) {
		t.Helper()
		path, err := store.Path(blob.Key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join("../../reference/vectors/storage", want.File))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("%s: bytes differ, size %d/%d checksum %s/%s", want.File, len(got), len(expected), blob.Checksum, want.Blob.Checksum)
		}
		if blob.Type() != want.Blob.Type() {
			t.Fatalf("content type %s/%s", blob.Type(), want.Blob.Type())
		}
	}
	for _, fixtures := range [][]fixture{data.Messages, data.Avatars, data.Logos} {
		for _, v := range fixtures {
			t.Run(v.Fixture, func(t *testing.T) {
				file, err := os.Open(filepath.Join("../../reference/reference/test/fixtures/files", v.Fixture))
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				blob, err := store.Stage(ctx, v.Fixture, v.Blob.Type(), file)
				if err != nil {
					t.Fatal(err)
				}
				if v.Preview != nil {
					preview, err := store.PreviewImage(ctx, blob)
					if err != nil {
						t.Fatal(err)
					}
					check(t, preview, *v.Preview)
				}
				for _, variant := range v.Variants {
					result, err := store.Representation(ctx, blob, typedValue(t, variant.Transformations).(Variation))
					if err != nil {
						t.Fatal(err)
					}
					check(t, result, variant)
				}
			})
		}
	}
}
