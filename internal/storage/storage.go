// Package storage implements Active Storage's database and local disk contracts.
package storage

import (
	"context"
	"crypto/md5"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

type Store struct {
	DB          *database.DB
	Verifier    rails.Verifier
	Root        string
	derivatives derivativeFlights
	purges      purgeContinuations
}
type DiskKey struct {
	Key         string  `json:"key"`
	Disposition string  `json:"disposition"`
	ContentType *string `json:"content_type"`
	ServiceName string  `json:"service_name"`
}
type DiskToken struct {
	Key           string  `json:"key"`
	ContentType   *string `json:"content_type"`
	ContentLength int64   `json:"content_length"`
	Checksum      string  `json:"checksum"`
	ServiceName   string  `json:"service_name"`
}

var ErrIntegrity = errors.New("checksum or size mismatch")

func New(db *database.DB, secrets *rails.Secrets, root string) *Store {
	files := os.Getenv("CAMPFIRE_FILES_PATH")
	if files == "" {
		files = filepath.Join(root, "files")
	}
	return &Store{DB: db, Verifier: secrets.AppVerifier("ActiveStorage"), Root: files}
}

func (s *Store) Path(key string) (string, error) {
	// Both shard names become path components, even when the key has no slash.
	if len(key) < 4 || strings.ContainsAny(key, "/\\\x00") || key[:2] == ".." || key[2:4] == ".." {
		return "", errors.New("invalid storage key")
	}
	return filepath.Join(s.Root, key[:2], key[2:4], key), nil
}

func (s *Store) FindSigned(ctx context.Context, token string) (database.Blob, error) {
	var id int64
	if err := s.Verifier.Verify(token, "blob_id", s.DB.Now(), &id); err != nil {
		return database.Blob{}, sql.ErrNoRows
	}
	return s.DB.Blob(ctx, id)
}

func (s *Store) Upload(ctx context.Context, token DiskToken, reader io.Reader) error {
	path, err := s.Path(token.Key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := md5.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, token.ContentLength+1))
	if err != nil {
		return err
	}
	if n != token.ContentLength ||
		base64.StdEncoding.EncodeToString(hash.Sum(nil)) != token.Checksum {
		return ErrIntegrity
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *Store) Stage(
	ctx context.Context,
	filename, contentType string,
	reader io.Reader,
) (database.Blob, error) {
	staged, err := s.StageFile(ctx, filename, contentType, reader)
	if err != nil {
		return database.Blob{}, err
	}
	defer staged.Discard()
	blob, err := s.DB.CreateBlob(ctx, staged.Blob)
	if err != nil {
		return database.Blob{}, err
	}
	staged.Commit(blob)
	return blob, nil
}

func (s *Store) StageFile(
	ctx context.Context,
	filename, contentType string,
	reader io.Reader,
) (*Staged, error) {
	key := rails.StorageKey()
	path, err := s.Path(key)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(path)
		}
	}()
	hash := md5.New()
	size, err := io.Copy(io.MultiWriter(f, hash), reader)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	b := database.Blob{
		Key:         key,
		Filename:    filename,
		ContentType: &contentType,
		ServiceName: "local",
		Metadata:    json.RawMessage(`{"identified":true}`),
		ByteSize:    size,
		Checksum:    base64.StdEncoding.EncodeToString(hash.Sum(nil)),
	}
	keep = true
	return &Staged{Blob: b, path: path}, nil
}

func (s *Store) DiskURL(b database.Blob, disposition string) (string, error) {
	ct := b.Type()
	served := ServingType(ct)
	if !Inline(ct) {
		disposition = "attachment"
	}
	if disposition != "attachment" {
		disposition = "inline"
	}
	token, err := s.Verifier.Generate(
		DiskKey{b.Key, Disposition(disposition, Filename(b.Filename)), &served, b.ServiceName},
		"blob_key",
		s.DB.Now().Add(5*time.Minute),
	)
	if err != nil {
		return "", err
	}
	return "/rails/active_storage/disk/" + Escape(
		token,
		false,
	) + "/" + Escape(
		Filename(b.Filename),
		true,
	), nil
}

func (s *Store) UploadURL(b database.Blob) (string, error) {
	token, err := s.Verifier.Generate(
		DiskToken{b.Key, b.ContentType, b.ByteSize, b.Checksum, b.ServiceName},
		"blob_token",
		s.DB.Now().Add(5*time.Minute),
	)
	return "/rails/active_storage/disk/" + Escape(token, false), err
}

func Filename(name string) string {
	return rails.Filename(name)
}

func ServingType(ct string) string {
	if slices.Contains(
		[]string{
			"text/html",
			"image/svg+xml",
			"application/postscript",
			"application/x-shockwave-flash",
			"text/xml",
			"application/xml",
			"application/xhtml+xml",
			"application/mathml+xml",
			"text/cache-manifest",
		},
		ct,
	) {
		return "application/octet-stream"
	}
	return ct
}

func Inline(ct string) bool {
	return slices.Contains(
		[]string{
			"image/webp",
			"image/avif",
			"image/png",
			"image/gif",
			"image/jpeg",
			"image/tiff",
			"image/bmp",
			"image/vnd.adobe.photoshop",
			"image/vnd.microsoft.icon",
			"application/pdf",
		},
		ct,
	)
}

func asciiWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func escape(text, keep string) string {
	var out strings.Builder
	for _, c := range []byte(text) {
		if asciiWord(c) || strings.ContainsRune(keep, rune(c)) {
			out.WriteByte(c)
		} else {
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func Escape(text string, path bool) string {
	keep := "-._~!$&'()*+,;=:@"
	if path {
		keep += "/"
	}
	return escape(text, keep)
}

//go:embed approximations.json
var approximationsJSON []byte

var approximations = func() map[string]string {
	var m map[string]string
	if err := json.Unmarshal(approximationsJSON, &m); err != nil {
		panic(err)
	}
	return m
}()

func Disposition(kind, name string) string {
	var ascii strings.Builder
	for _, c := range name {
		if c < 128 {
			ascii.WriteRune(c)
		} else if v, ok := approximations[string(c)]; ok {
			ascii.WriteString(v)
		} else {
			ascii.WriteByte('?')
		}
	}
	return kind + `; filename="` + escape(
		ascii.String(),
		" !#$+.^_`|~-",
	) + `"; filename*=UTF-8''` + escape(
		name,
		"!#$&+.^_`|~-",
	)
}

func Variable(ct string) bool {
	return slices.Contains(
		[]string{
			"image/png",
			"image/gif",
			"image/jpeg",
			"image/tiff",
			"image/webp",
			"image/avif",
			"image/heic",
			"image/heif",
		},
		ct,
	)
}
