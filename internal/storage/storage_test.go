package storage

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestFilenameVectors(t *testing.T) {
	data, err := os.ReadFile("../../reference/vectors/storage.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Filenames []struct {
			Input                         string `json:"input_hex"`
			Sanitized, Inline, Attachment string
			Escaped                       string `json:"escaped_path"`
		}
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors.Filenames {
		raw, err := hex.DecodeString(v.Input)
		if err != nil {
			t.Fatal(err)
		}
		name := Filename(strings.ToValidUTF8(string(raw), "�"))
		if name != v.Sanitized {
			t.Errorf("sanitize %q: %q want %q", raw, name, v.Sanitized)
		}
		for kind, want := range map[string]string{"inline": v.Inline, "attachment": v.Attachment} {
			if got := Disposition(kind, name); got != want {
				t.Errorf("disposition %q: %q want %q", name, got, want)
			}
		}
		if got := Escape(name, true); got != v.Escaped {
			t.Errorf("escape %q: %q want %q", name, got, v.Escaped)
		}
	}
}

func TestUploadIntegrityAndSigning(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret, err := rails.NewSecrets("storage-test")
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, secret, root)
	ctx := context.Background()
	content := "an uploaded file"
	sum := md5.Sum([]byte(content))
	ct := "text/plain"
	b, err := s.DB.CreateBlob(
		ctx,
		database.Blob{
			Filename:    "notes.txt",
			ContentType: &ct,
			ByteSize:    int64(len(content)),
			Checksum:    base64.StdEncoding.EncodeToString(sum[:]),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	token := DiskToken{b.Key, b.ContentType, b.ByteSize, b.Checksum, b.ServiceName}
	if err = s.Upload(ctx, token, strings.NewReader("wrong")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bad checksum: %v", err)
	}
	path, err := s.Path(b.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid file retained: %v", err)
	}
	if err = s.Upload(ctx, token, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("stored file: %q %v", data, err)
	}
	found, err := s.FindSigned(ctx, SignedID(s.Verifier, b))
	if err != nil || found.ID != b.ID {
		t.Fatal(found, err)
	}
	url, err := s.DiskURL(b, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "/rails/active_storage/disk/") {
		t.Fatal(url)
	}
	signed, err := s.Verifier.Generate(token, "blob_token", db.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var decoded DiskToken
	if err = s.Verifier.Verify(signed, "blob_key", db.Now(), &decoded); err == nil {
		t.Fatal("upload token accepted as download key")
	}
	if err = s.Verifier.Verify(signed, "blob_token", db.Now().Add(2*time.Minute), &decoded); err == nil {
		t.Fatal("expired token accepted")
	}
	for _, key := range []string{"../escape", "abcd/../../escape", "abcd\\escape", ".", "..escape", "....escape", "ab..escape"} {
		if _, err = s.Path(key); err == nil {
			t.Errorf("unsafe key accepted: %q", key)
		}
	}
	if got, err := s.Path("abcd-legacy.key"); err != nil ||
		got != filepath.Join(s.Root, "ab", "cd", "abcd-legacy.key") {
		t.Fatalf("valid legacy key lost its reference storage layout: %q %v", got, err)
	}
}

func TestTrackedVariantIsReusable(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, err := rails.NewSecrets("variants-test")
	if err != nil {
		t.Fatal(err)
	}
	store := New(db, secrets, root)
	ctx := context.Background()
	source, err := os.Open("../../reference/reference/test/fixtures/files/moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	b, err := store.Stage(ctx, "moon.jpg", "image/jpeg", source)
	if err != nil {
		t.Fatal(err)
	}
	variation := Resize(128, 128, "webp")
	results := make(chan database.Blob, 2)
	failures := make(chan error, 2)
	// Separate stores and SQLite connections cannot share derivative flights.
	otherDB, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	for _, competing := range []*Store{store, New(otherDB, secrets, root)} {
		go func() { result, err := competing.Variant(ctx, b, variation); results <- result; failures <- err }()
	}
	first, second := <-results, <-results
	for range 2 {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	if first.ID != second.ID || first.Type() != "image/webp" {
		t.Fatalf("variant not shared: %+v %+v", first, second)
	}
	path, err := store.Path(first.Key)
	if err != nil {
		t.Fatal(err)
	}
	w, h, err := imageProcess(path, "", 0, 0)
	if err != nil || w > 128 || h > 128 || w < 1 || h < 1 {
		t.Fatalf("variant dimensions: %d %d %v", w, h, err)
	}
	var count int
	if err = db.Read.QueryRow("SELECT count(*) FROM active_storage_blobs").Scan(&count); err != nil ||
		count != 2 {
		t.Fatalf("leaked variant blobs: %d %v", count, err)
	}
	if files := storedFiles(t, store.Root); len(files) != 2 {
		t.Fatalf("leaked variant files: %v", files)
	}
	for _, table := range []string{"active_storage_variant_records", "active_storage_attachments"} {
		if err = db.Read.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil ||
			count != 1 {
			t.Fatalf("duplicate variant graph in %s: %d %v", table, count, err)
		}
	}
}

func TestRepresentationURLIncludesDefaultFormat(t *testing.T) {
	db, err := database.Open(t.TempDir()+"/db.sqlite3", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secrets, err := rails.NewSecrets("representation-test")
	if err != nil {
		t.Fatal(err)
	}
	store := New(db, secrets, t.TempDir())
	contentType := "image/jpeg"
	blob := database.Blob{ID: 7, Filename: "photo.jpg", ContentType: &contentType}
	path, err := RepresentationURL(store.Verifier, blob, Resize(1200, 800, ""))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(path, "/")
	variation, err := store.DecodeVariation(parts[len(parts)-2])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := variation.MarshalJSON()
	if err != nil || string(raw) != `{"format":"jpg","resize_to_limit":[1200,800]}` {
		t.Fatalf("signed transformations: %s, %v", raw, err)
	}
}
