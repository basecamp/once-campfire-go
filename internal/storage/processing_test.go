package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

func TestDetachedAttachmentProcessingDeadline(t *testing.T) {
	for _, boundary := range []string{"admission", "database"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			db, err := database.Open(filepath.Join(root, "processing.sqlite3"), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			secrets, err := rails.NewSecrets("processing-test")
			if err != nil {
				t.Fatal(err)
			}
			store := New(db, secrets, root)
			contentType := "text/plain"
			if boundary == "admission" {
				contentType = "image/png"
			}
			blob, err := store.Stage(context.Background(), "attachment", contentType, strings.NewReader("contents"))
			if err != nil {
				t.Fatal(err)
			}
			var release func()
			if boundary == "database" {
				writer, err := db.Write.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				release = func() { writer.Close() }
			}
			originalSlots := mediaSlots
			defer func() { mediaSlots = originalSlots }()
			synctest.Test(t, func(t *testing.T) {
				if boundary == "admission" {
					// Bubble-owned admission lets the real deadline run without
					// a two-minute wall-clock test. No native work can start.
					mediaSlots = make(chan struct{}, cap(originalSlots))
					for range cap(mediaSlots) {
						mediaSlots <- struct{}{}
					}
					release = func() { <-mediaSlots }
				}
				parent, disconnect := context.WithCancel(context.Background())
				disconnect()
				finished := make(chan struct{})
				go func() {
					select {
					case <-finished:
					case <-time.After(3 * time.Minute):
						// Unstick the original defect so it reports a missed
						// deadline instead of deadlocking the test bubble.
						release()
					}
				}()
				start := time.Now()
				_, err := store.ProcessAttachment(context.WithoutCancel(parent), blob)
				elapsed := time.Since(start)
				close(finished)
				if !errors.Is(err, context.DeadlineExceeded) || elapsed > 2*time.Minute {
					t.Fatalf("detached processing: elapsed=%s error=%v", elapsed, err)
				}
			})
			stored, err := db.Blob(context.Background(), blob.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(stored.Metadata) != string(blob.Metadata) {
				t.Fatalf("timed-out processing changed metadata: %s", stored.Metadata)
			}
		})
	}
}
