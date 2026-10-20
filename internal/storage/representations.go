package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
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

func (s *Store) checkedFile(ctx context.Context, b database.Blob) (string, error) {
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

// analyzeMetadata operates on owned bytes without inserting or updating a blob.
// Derivatives are analyzed while staged, before their graph is committed.
func (s *Store) analyzeMetadata(ctx context.Context, b database.Blob) (database.Blob, error) {
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
	b.Metadata = raw
	return b, ctx.Err()
}

func (s *Store) Analyze(ctx context.Context, b database.Blob) (database.Blob, error) {
	b, err := s.analyzeMetadata(ctx, b)
	if err != nil {
		return b, err
	}
	err = s.DB.UpdateBlobMetadata(ctx, b.ID, b.Metadata)
	return b, err
}

func (s *Store) Variant(ctx context.Context, b database.Blob, variation Variation) (database.Blob, error) {
	defaultFormat := DefaultFormat(b)
	v := variation.DefaultFormat(defaultFormat)
	if v.Get("format") == Symbol(defaultFormat) && variation.Get("format") == nil {
		v[0].Value = defaultFormat
	}
	digest := v.Digest()
	if existing, err := s.DB.VariantBlob(ctx, b.ID, digest); !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	return s.derivative(ctx, derivativeKey{blob: b.ID, digest: digest},
		func(ctx context.Context) (database.Blob, error) { return s.DB.VariantBlob(ctx, b.ID, digest) },
		func(ctx context.Context) (database.Blob, error) { return s.createVariant(ctx, b, v, digest) })
}

func (s *Store) createVariant(
	ctx context.Context,
	b database.Blob,
	v Variation,
	digest string,
) (database.Blob, error) {
	format, err := v.Format()
	if err != nil {
		return database.Blob{}, err
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
			return database.Blob{}, fmt.Errorf("unsupported transformation %s", e.Key)
		}
		args, ok := e.Value.([]any)
		if !ok || len(args) != 2 {
			return database.Blob{}, errors.New("invalid resize dimensions")
		}
		dimensions := []*int{&width, &height}
		for i, arg := range args {
			if arg == nil {
				continue
			}
			n, ok := arg.(int64)
			if !ok || n <= 0 || n > 2147483647 {
				return database.Blob{}, errors.New("invalid resize dimension")
			}
			*dimensions[i] = int(n)
		}
		if width == 0 && height == 0 {
			return database.Blob{}, errors.New("missing resize dimensions")
		}
	}
	input, err := s.checkedFile(ctx, b)
	if err != nil {
		return database.Blob{}, err
	}
	temporary := filepath.Join(s.Root, ".processing")
	if err = os.MkdirAll(temporary, 0o755); err != nil {
		return database.Blob{}, err
	}
	file, err := os.CreateTemp(temporary, "variant-*."+format)
	if err != nil {
		return database.Blob{}, err
	}
	file.Close()
	defer os.Remove(file.Name())
	select {
	case mediaSlots <- struct{}{}:
	case <-ctx.Done():
		return database.Blob{}, ctx.Err()
	}
	_, _, err = imageProcess(input, file.Name(), width, height)
	<-mediaSlots
	if err != nil {
		return database.Blob{}, err
	}
	file, err = os.Open(file.Name())
	if err != nil {
		return database.Blob{}, err
	}
	defer file.Close()
	name := strings.TrimSuffix(filepath.Base(b.Filename), filepath.Ext(b.Filename)) + "." + format
	ct := "image/" + format
	if format == "jpg" {
		ct = "image/jpeg"
	}
	image, err := s.StageFile(ctx, name, ct, file)
	if err != nil {
		return database.Blob{}, err
	}
	defer image.Discard()
	image.Blob, err = s.analyzeMetadata(ctx, image.Blob)
	if err != nil {
		return database.Blob{}, err
	}
	commit, err := s.DB.LinkVariant(ctx, b.ID, digest, image.Blob)
	if err != nil {
		return database.Blob{}, err
	}
	if commit.Created {
		image.Commit(commit.Blob)
	}
	return commit.Blob, nil
}
