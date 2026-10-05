package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
)

var mediaSlots = make(chan struct{}, 4)

func (s *Store) checkedFile(ctx context.Context, b Blob) (string, error) {
	path, err := s.Path(b.Key)
	if err != nil {
		return "", err
	}
	if b.Checksum == "" {
		return path, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := md5.New()
	if _, err = io.Copy(sum, file); err != nil {
		return "", err
	}
	if base64.StdEncoding.EncodeToString(sum.Sum(nil)) != b.Checksum {
		return "", ErrIntegrity
	}
	return path, ctx.Err()
}
func (s *Store) Analyze(ctx context.Context, b Blob) (Blob, error) {
	metadata := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(b.Metadata))
	decoder.UseNumber()
	decoder.Decode(&metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if strings.HasPrefix(b.Type(), "image") {
		path, err := s.checkedFile(ctx, b)
		if err != nil {
			return b, err
		}
		select {
		case mediaSlots <- struct{}{}:
		case <-ctx.Done():
			return b, ctx.Err()
		}
		width, height, err := imageProcess(path, "", 0, 0)
		<-mediaSlots
		if err == nil {
			metadata["width"] = width
			metadata["height"] = height
		}
	}
	if strings.HasPrefix(b.Type(), "video") || strings.HasPrefix(b.Type(), "audio") {
		path, err := s.checkedFile(ctx, b)
		if err != nil {
			return b, err
		}
		select {
		case mediaSlots <- struct{}{}:
		case <-ctx.Done():
			return b, ctx.Err()
		}
		data, err := probe(ctx, path)
		<-mediaSlots
		if err != nil {
			return b, err
		}
		extracted, err := mediaMetadata(data, strings.HasPrefix(b.Type(), "video"))
		if err != nil {
			return b, err
		}
		for key, value := range extracted {
			metadata[key] = value
		}
	}
	metadata["analyzed"] = true
	raw, err := json.Marshal(metadata)
	if err != nil {
		return b, err
	}
	err = s.DB.Transaction(ctx, func(tx *database.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE active_storage_blobs SET metadata=? WHERE id=?", string(raw), b.ID); err != nil {
			return err
		}
		now := database.Stamp(s.DB.Now())
		for _, record := range []struct{ kind, table string }{{"Message", "messages"}, {"User", "users"}, {"Account", "accounts"}} {
			if _, err := tx.ExecContext(ctx, "UPDATE "+record.table+" SET updated_at=? WHERE id IN (SELECT record_id FROM active_storage_attachments WHERE blob_id=? AND record_type=?)", now, b.ID, record.kind); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE rooms SET updated_at=? WHERE id IN (SELECT room_id FROM messages WHERE id IN (SELECT record_id FROM active_storage_attachments WHERE blob_id=? AND record_type='Message'))", now, b.ID)
		return err
	})
	b.Metadata = raw
	return b, err
}
func (s *Store) existingVariant(ctx context.Context, blob int64, digest string) (Blob, error) {
	return scanBlob(s.DB.Read.QueryRowContext(ctx, "SELECT "+columns+" FROM active_storage_blobs b JOIN active_storage_attachments a ON a.blob_id=b.id JOIN active_storage_variant_records v ON v.id=a.record_id WHERE a.record_type='ActiveStorage::VariantRecord' AND a.name='image' AND v.blob_id=? AND v.variation_digest=? LIMIT 1", blob, digest))
}

// variationFor is the variation with b's default format filled in, whose digest the variant is
// recorded under (the reference's Storage::variation_for).
func variationFor(b Blob, variation Variation) Variation {
	defaultFormat := b.DefaultFormat()
	v := variation.DefaultFormat(defaultFormat)
	if v.Get("format") == Symbol(defaultFormat) && variation.Get("format") == nil {
		v[0].Value = defaultFormat
	}
	return v
}

// VariantDigest is the variation digest b's variant is recorded under.
func VariantDigest(b Blob, variation Variation) string { return variationFor(b, variation).Digest() }

func (s *Store) Variant(ctx context.Context, b Blob, variation Variation) (Blob, error) {
	v := variationFor(b, variation)
	digest := v.Digest()
	if existing, err := s.existingVariant(ctx, b.ID, digest); err == nil {
		return existing, nil
	} else if !errors.Is(err, database.ErrNoRows) {
		return Blob{}, err
	}
	format, err := v.Format()
	if err != nil {
		return Blob{}, err
	}
	width, height := 0, 0
	for _, e := range v {
		if e.Key == "format" {
			continue
		}
		if e.Value == nil || e.Value == false {
			continue
		}
		if e.Key != "resize_to_limit" {
			return Blob{}, fmt.Errorf("unsupported transformation %s", e.Key)
		}
		args, ok := e.Value.([]any)
		if !ok || len(args) != 2 {
			return Blob{}, errors.New("invalid resize dimensions")
		}
		dimensions := []*int{&width, &height}
		for i, arg := range args {
			if arg == nil {
				continue
			}
			n, ok := arg.(int64)
			if !ok || n <= 0 || n > 2147483647 {
				return Blob{}, errors.New("invalid resize dimension")
			}
			*dimensions[i] = int(n)
		}
		if width == 0 && height == 0 {
			return Blob{}, errors.New("missing resize dimensions")
		}
	}
	input, err := s.checkedFile(ctx, b)
	if err != nil {
		return Blob{}, err
	}
	temporary := filepath.Join(s.Root, ".processing")
	if err = os.MkdirAll(temporary, 0755); err != nil {
		return Blob{}, err
	}
	file, err := os.CreateTemp(temporary, "variant-*."+format)
	if err != nil {
		return Blob{}, err
	}
	file.Close()
	defer os.Remove(file.Name())
	select {
	case mediaSlots <- struct{}{}:
	case <-ctx.Done():
		return Blob{}, ctx.Err()
	}
	_, _, err = imageProcess(input, file.Name(), width, height)
	<-mediaSlots
	if err != nil {
		return Blob{}, err
	}
	file, err = os.Open(file.Name())
	if err != nil {
		return Blob{}, err
	}
	defer file.Close()
	name := strings.TrimSuffix(filepath.Base(b.Filename), filepath.Ext(b.Filename)) + "." + format
	ct := "image/" + format
	if format == "jpg" {
		ct = "image/jpeg"
	}
	image, err := s.Stage(ctx, name, ct, file)
	if err != nil {
		return Blob{}, err
	}
	image, err = s.Analyze(ctx, image)
	if err != nil {
		s.discard(ctx, image)
		return Blob{}, err
	}
	won := false
	err = s.DB.Transaction(ctx, func(tx *database.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT INTO active_storage_variant_records(blob_id,variation_digest) VALUES (?,?) ON CONFLICT(blob_id,variation_digest) DO NOTHING", b.ID, digest)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'ActiveStorage::VariantRecord',?,'image',?)", image.ID, id, database.Stamp(s.DB.Now()))
		won = err == nil
		return err
	})
	if err != nil || !won {
		s.discard(ctx, image)
		if err != nil {
			return Blob{}, err
		}
		return s.existingVariant(ctx, b.ID, digest)
	}
	return image, nil
}
func (s *Store) discard(ctx context.Context, b Blob) {
	result, err := s.DB.Write.ExecContext(ctx, "DELETE FROM active_storage_blobs WHERE id=? AND NOT EXISTS(SELECT 1 FROM active_storage_attachments WHERE blob_id=?)", b.ID, b.ID)
	if err != nil {
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return
	}
	if path, err := s.Path(b.Key); err == nil {
		os.Remove(path)
	}
}
func (s *Store) RepresentationURL(b Blob, v Variation) (string, error) {
	// ActiveStorage::Blob#variation defaults the format before signing the URL.
	if v.Get("format") == nil {
		v = append(Variation{{Key: "format", Value: b.DefaultFormat()}}, v...)
	}
	key, err := s.VariationKey(v)
	if err != nil {
		return "", err
	}
	return "/rails/active_storage/representations/redirect/" + Escape(s.SignedID(b), false) + "/" + Escape(key, false) + "/" + Escape(Filename(b.Filename), true), nil
}
