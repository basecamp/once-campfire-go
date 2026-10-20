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

var ffmpegAvailable = sync.OnceValue(
	func() bool { return exec.Command("ffmpeg", "-version").Run() == nil },
)

func Previewable(ct string) bool { return strings.HasPrefix(ct, "video") && ffmpegAvailable() }
func (s *Store) PreviewImage(ctx context.Context, b database.Blob) (database.Blob, error) {
	if image, err := s.DB.AttachedBlob(ctx, "ActiveStorage::Blob", b.ID, "preview_image"); !errors.Is(
		err,
		sql.ErrNoRows,
	) {
		return image, err
	}
	return s.derivative(ctx, derivativeKey{blob: b.ID},
		func(ctx context.Context) (database.Blob, error) {
			return s.DB.AttachedBlob(ctx, "ActiveStorage::Blob", b.ID, "preview_image")
		},
		func(ctx context.Context) (database.Blob, error) { return s.createPreviewImage(ctx, b) })
}

func (s *Store) createPreviewImage(ctx context.Context, b database.Blob) (database.Blob, error) {
	if !Previewable(b.Type()) {
		return database.Blob{}, errors.New("unpreviewable blob")
	}
	input, err := s.checkedFile(ctx, b)
	if err != nil {
		return database.Blob{}, err
	}
	select {
	case mediaSlots <- struct{}{}:
	case <-ctx.Done():
		return database.Blob{}, ctx.Err()
	}
	processCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	command := exec.CommandContext(
		processCtx,
		"ffmpeg",
		"-i",
		input,
		"-vf",
		`select=eq(n\,0)+eq(key\,1)+gt(scene\,0.015),loop=loop=-1:size=2,trim=start_frame=1`,
		"-frames:v",
		"1",
		"-f",
		"image2",
		"-",
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	cancel()
	<-mediaSlots
	if err != nil {
		return database.Blob{}, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	name := strings.TrimSuffix(Filename(b.Filename), filepath.Ext(b.Filename)) + ".jpg"
	image, err := s.StageFile(ctx, name, "image/jpeg", bytes.NewReader(raw))
	if err != nil {
		return database.Blob{}, err
	}
	defer image.Discard()
	image.Blob, err = s.analyzeMetadata(ctx, image.Blob)
	if err != nil {
		return database.Blob{}, err
	}
	commit, err := s.DB.LinkPreview(ctx, b.ID, image.Blob)
	if err != nil {
		return database.Blob{}, err
	}
	if commit.Created {
		image.Commit(commit.Blob)
	}
	return commit.Blob, nil
}

func (s *Store) Representation(ctx context.Context, b database.Blob, v Variation) (database.Blob, error) {
	if Previewable(b.Type()) {
		image, err := s.PreviewImage(ctx, b)
		if err != nil {
			return database.Blob{}, err
		}
		if len(v) == 0 {
			return image, nil
		}
		return s.Variant(ctx, image, v)
	}
	if Variable(b.Type()) {
		return s.Variant(ctx, b, v)
	}
	return database.Blob{}, errors.New("unrepresentable blob")
}

func (s *Store) ProcessAttachment(ctx context.Context, b database.Blob) (database.Blob, error) {
	// Cover admission and database waits as well as the 30s analysis and 60s
	// preview subprocesses. Native libvips work still cannot be preempted.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
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
