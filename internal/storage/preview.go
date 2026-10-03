package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

var ffmpegAvailable = sync.OnceValue(func() bool { return exec.Command("ffmpeg", "-version").Run() == nil })

func Previewable(ct string) bool { return strings.HasPrefix(ct, "video") && ffmpegAvailable() }
func (s *Store) PreviewImage(ctx context.Context, b Blob) (Blob, error) {
	if image, err := s.Attached(ctx, "ActiveStorage::Blob", b.ID, "preview_image"); err == nil {
		return image, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Blob{}, err
	}
	if !Previewable(b.Type()) {
		return Blob{}, errors.New("unpreviewable blob")
	}
	input, err := s.checkedFile(ctx, b)
	if err != nil {
		return Blob{}, err
	}
	select {
	case mediaSlots <- struct{}{}:
	case <-ctx.Done():
		return Blob{}, ctx.Err()
	}
	processCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	command := exec.CommandContext(processCtx, "ffmpeg", "-i", input, "-vf", `select=eq(n\,0)+eq(key\,1)+gt(scene\,0.015),loop=loop=-1:size=2,trim=start_frame=1`, "-frames:v", "1", "-f", "image2", "-")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	cancel()
	<-mediaSlots
	if err != nil {
		return Blob{}, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	name := strings.TrimSuffix(Filename(b.Filename), filepath.Ext(b.Filename)) + ".jpg"
	image, err := s.Stage(ctx, name, "image/jpeg", bytes.NewReader(raw))
	if err != nil {
		return Blob{}, err
	}
	image, err = s.Analyze(ctx, image)
	if err != nil {
		s.discard(ctx, image)
		return Blob{}, err
	}
	won := false
	err = s.DB.Transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM active_storage_attachments WHERE record_type='ActiveStorage::Blob' AND record_id=? AND name='preview_image'", b.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO active_storage_attachments(blob_id,record_type,record_id,name,created_at) VALUES (?,'ActiveStorage::Blob',?,'preview_image',?)", image.ID, b.ID, database.Stamp(s.DB.Now()))
		won = err == nil
		return err
	})
	if err != nil || !won {
		s.discard(ctx, image)
		if err != nil {
			return Blob{}, err
		}
		return s.Attached(ctx, "ActiveStorage::Blob", b.ID, "preview_image")
	}
	return image, nil
}
func (s *Store) Representation(ctx context.Context, b Blob, v Variation) (Blob, error) {
	if Previewable(b.Type()) {
		image, err := s.PreviewImage(ctx, b)
		if err != nil {
			return Blob{}, err
		}
		if len(v) == 0 {
			return image, nil
		}
		return s.Variant(ctx, image, v)
	}
	if Variable(b.Type()) {
		return s.Variant(ctx, b, v)
	}
	return Blob{}, errors.New("unrepresentable blob")
}
func (s *Store) ProcessAttachment(ctx context.Context, b Blob) (Blob, error) {
	b, err := s.Analyze(ctx, b)
	if err != nil {
		return b, err
	}
	if Previewable(b.Type()) {
		_, err = s.Representation(ctx, b, Variation{{Key: "format", Value: Symbol("webp")}})
	} else if Variable(b.Type()) {
		_, err = s.Variant(ctx, b, Resize(1200, 800, ""))
	}
	return b, err
}
