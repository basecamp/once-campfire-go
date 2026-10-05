// Package storage implements Active Storage's database and local disk contracts.
package storage

import (
	"context"
	"crypto/md5"
	"crypto/rand"
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
	DB       *database.DB
	Verifier rails.Verifier
	Root     string
}
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
	return &Store{db, secrets.AppVerifier("ActiveStorage"), files}
}
func Key() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	key := make([]byte, 0, 28)
	var random [64]byte
	for len(key) < 28 {
		if _, err := rand.Read(random[:]); err != nil {
			panic(err)
		}
		for _, b := range random {
			if b < 252 {
				key = append(key, alphabet[int(b)%36])
				if len(key) == 28 {
					break
				}
			}
		}
	}
	return string(key)
}
func (s *Store) Path(key string) (string, error) {
	if len(key) < 4 || strings.ContainsAny(key, "/\\\x00") || key == "." || key == ".." {
		return "", errors.New("invalid storage key")
	}
	return filepath.Join(s.Root, key[:2], key[2:4], key), nil
}
func scanBlob(row *sql.Row) (Blob, error) { return scanBlobFields(row.Scan) }
func scanBlobFields(scan func(...any) error) (Blob, error) {
	var b Blob
	var metadata sql.NullString
	var checksum sql.NullString
	err := scan(&b.ID, &b.Key, &b.Filename, &b.ContentType, &metadata, &b.ServiceName, &b.ByteSize, &checksum, &b.CreatedAt)
	b.Metadata = json.RawMessage(metadata.String)
	if !json.Valid(b.Metadata) {
		b.Metadata = json.RawMessage("{}")
	}
	b.Checksum = checksum.String
	return b, err
}

const columns = "b.id,b.key,b.filename,b.content_type,b.metadata,b.service_name,b.byte_size,b.checksum,b.created_at"

func (s *Store) Blob(ctx context.Context, id int64) (Blob, error) {
	return scanBlob(s.DB.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM active_storage_blobs b WHERE b.id=?", id))
}
func (s *Store) Attached(ctx context.Context, kind string, id int64, name string) (Blob, error) {
	return scanBlob(s.DB.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id WHERE a.record_type=? AND a.record_id=? AND a.name=? ORDER BY a.id LIMIT 1", kind, id, name))
}

// AttachedMany is Attached for several records in one query: each record's first
// attachment by attachment ID.
func (s *Store) AttachedMany(ctx context.Context, kind string, ids []int64, name string) (map[int64]Blob, error) {
	result := make(map[int64]Blob)
	if len(ids) == 0 {
		return result, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Read.QueryContext(ctx, "SELECT a.record_id,"+columns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id WHERE a.record_type=? AND a.record_id IN (SELECT value FROM json_each(?)) AND a.name=? ORDER BY a.record_id,a.id", kind, string(raw), name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var record int64
		b, err := scanBlobFields(func(fields ...any) error { return rows.Scan(append([]any{&record}, fields...)...) })
		if err != nil {
			return nil, err
		}
		if _, found := result[record]; !found {
			result[record] = b
		}
	}
	return result, rows.Err()
}
func (s *Store) Create(ctx context.Context, b Blob) (Blob, error) {
	if b.Key == "" {
		b.Key = Key()
	}
	if b.ServiceName == "" {
		b.ServiceName = "local"
	}
	if len(b.Metadata) == 0 {
		b.Metadata = json.RawMessage("{}")
	}
	b.CreatedAt = database.Stamp(s.DB.Now())
	result, err := s.DB.Write.ExecContext(ctx, "INSERT INTO active_storage_blobs(key,filename,content_type,metadata,service_name,byte_size,checksum,created_at) VALUES (?,?,?,?,?,?,?,?)", b.Key, b.Filename, b.ContentType, string(b.Metadata), b.ServiceName, b.ByteSize, b.Checksum, b.CreatedAt)
	if err != nil {
		return b, err
	}
	b.ID, err = result.LastInsertId()
	return b, err
}
func (s *Store) SignedID(b Blob) string {
	token, err := s.Verifier.Generate(b.ID, "blob_id", time.Time{})
	if err != nil {
		panic(err)
	}
	return token
}
func (s *Store) FindSigned(ctx context.Context, token string) (Blob, error) {
	var id int64
	if err := s.Verifier.Verify(token, "blob_id", s.DB.Now(), &id); err != nil {
		return Blob{}, sql.ErrNoRows
	}
	return s.Blob(ctx, id)
}
func (s *Store) Upload(ctx context.Context, token DiskToken, reader io.Reader) error {
	path, err := s.Path(token.Key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
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
	if n != token.ContentLength || base64.StdEncoding.EncodeToString(hash.Sum(nil)) != token.Checksum {
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
func (s *Store) Stage(ctx context.Context, filename, contentType string, reader io.Reader) (Blob, error) {
	staged, err := s.StageFile(ctx, filename, contentType, reader)
	if err != nil {
		return Blob{}, err
	}
	return staged.Save(ctx)
}
func (s *Store) StageFile(ctx context.Context, filename, contentType string, reader io.Reader) (*Staged, error) {
	key := Key()
	path, err := s.Path(key)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
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
	b := Blob{Key: key, Filename: filename, ContentType: &contentType, ServiceName: "local", Metadata: json.RawMessage(`{"identified":true}`), ByteSize: size, Checksum: base64.StdEncoding.EncodeToString(hash.Sum(nil))}
	keep = true
	return &Staged{Blob: b, path: path, store: s}, nil
}
func (s *Store) Attach(ctx context.Context, b Blob, kind string, id int64, name string) error {
	var blobs []int64
	err := s.DB.Transaction(ctx, func(tx *sql.Tx) error {
		var err error
		blobs, err = database.AttachmentBlobIDs(ctx, tx, "record_type=? AND record_id=? AND name=?", kind, id, name)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM active_storage_attachments WHERE record_type=? AND record_id=? AND name=?", kind, id, name); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,?,?,?,?)", b.ID, kind, id, name, database.Stamp(s.DB.Now()))
		return err
	})
	if err == nil {
		s.DB.PurgeDetached(blobs)
	}
	return err
}
func (s *Store) DiskURL(b Blob, disposition string) (string, error) {
	ct := b.Type()
	served := ServingType(ct)
	if !Inline(ct) {
		disposition = "attachment"
	}
	if disposition != "attachment" {
		disposition = "inline"
	}
	token, err := s.Verifier.Generate(DiskKey{b.Key, Disposition(disposition, Filename(b.Filename)), &served, b.ServiceName}, "blob_key", s.DB.Now().Add(5*time.Minute))
	if err != nil {
		return "", err
	}
	return "/rails/active_storage/disk/" + Escape(token, false) + "/" + Escape(Filename(b.Filename), true), nil
}
func (s *Store) UploadURL(b Blob) (string, error) {
	token, err := s.Verifier.Generate(DiskToken{b.Key, b.ContentType, b.ByteSize, b.Checksum, b.ServiceName}, "blob_token", s.DB.Now().Add(5*time.Minute))
	return "/rails/active_storage/disk/" + Escape(token, false), err
}
func (s *Store) BlobURL(b Blob) string {
	return "/rails/active_storage/blobs/redirect/" + Escape(s.SignedID(b), false) + "/" + Escape(Filename(b.Filename), true)
}
func (b Blob) Type() string {
	if b.ContentType == nil {
		return ""
	}
	return *b.ContentType
}
func Filename(name string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune("\u202e%$|:;/<>?*\"\t\r\n\\", c) {
			return '-'
		}
		return c
	}, strings.Trim(name, "\x00\t\n\v\f\r "))
}
func ServingType(ct string) string {
	if slices.Contains([]string{"text/html", "image/svg+xml", "application/postscript", "application/x-shockwave-flash", "text/xml", "application/xml", "application/xhtml+xml", "application/mathml+xml", "text/cache-manifest"}, ct) {
		return "application/octet-stream"
	}
	return ct
}
func Inline(ct string) bool {
	return slices.Contains([]string{"image/webp", "image/avif", "image/png", "image/gif", "image/jpeg", "image/tiff", "image/bmp", "image/vnd.adobe.photoshop", "image/vnd.microsoft.icon", "application/pdf"}, ct)
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
	return kind + `; filename="` + escape(ascii.String(), " !#$+.^_`|~-") + `"; filename*=UTF-8''` + escape(name, "!#$&+.^_`|~-")
}

func Variable(ct string) bool {
	return slices.Contains([]string{"image/png", "image/gif", "image/jpeg", "image/tiff", "image/webp", "image/avif", "image/heic", "image/heif"}, ct)
}
