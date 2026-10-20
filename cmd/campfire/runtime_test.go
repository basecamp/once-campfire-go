package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/jobs"
)

// The actual process owner must join every closer, retain cancelled purge work,
// and keep SQLite usable by admitted work until the final cleanup finishes.
func TestRuntimeJoinsWorkAndRetainedPurgesBeforeClosingDatabase(t *testing.T) {
	root := t.TempDir()
	config := serverConfig{
		Database:       databaseConfig{Path: filepath.Join(root, "db.sqlite3"), Storage: root, Secret: "runtime-test", Readers: 2},
		JobConcurrency: 1,
	}
	app, err := openApplication(config)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()
	blob, err := app.storage.Stage(ctx, "detached.txt", "text/plain", strings.NewReader("detached"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := app.storage.Path(blob.Key)
	if err != nil {
		t.Fatal(err)
	}
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if admission := app.jobs.Enqueue(shutdownTask{db: app.db, entered: entered, cancelled: cancelled, release: release, result: result}); admission != jobs.Accepted {
		t.Fatal(admission)
	}
	<-entered
	if err = app.HTTP.MessageCommands.Cleanup.Detached([]int64{blob.ID}); err != nil {
		t.Fatal(err)
	}
	jobsJoined := make(chan struct{})
	go func() { app.jobs.Close(0); close(jobsJoined) }()
	<-cancelled
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- app.Close() }()
	}
	// Observe the public intake cutoff rather than a private call sequence.
	deadline := time.Now().Add(2 * time.Second)
	for {
		response := httptest.NewRecorder()
		app.HTTP.ServeHTTP(response, httptest.NewRequest("GET", "/up", nil))
		if response.Code == 503 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("intake did not stop")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closed:
		t.Fatalf("runtime released resources while work survived: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err = app.db.Write.PingContext(ctx); err != nil {
		t.Fatalf("SQLite closed under work: %v", err)
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatalf("admitted cleanup lost SQLite: %v", err)
	}
	for range 2 {
		select {
		case err = <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("runtime did not join")
		}
	}
	<-jobsJoined
	if err = app.db.Read.PingContext(ctx); err == nil {
		t.Fatal("runtime left database open")
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled purge left file: %v", err)
	}
	// Audit persistence after the real owner has closed all connections.
	db, err := database.Open(config.Database.Path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Blob(ctx, blob.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cancelled purge lost its detached root: %v", err)
	}
}

type shutdownTask struct {
	db                          *database.DB
	entered, cancelled, release chan struct{}
	result                      chan error
}

func (shutdownTask) Queue() string { return "purge" }
func (task shutdownTask) Run(ctx context.Context) error {
	close(task.entered)
	<-ctx.Done()
	close(task.cancelled)
	<-task.release
	err := task.db.Write.PingContext(context.Background())
	task.result <- err
	return err
}

func TestRuntimeOpeningFailureDoesNotStartAnApplication(t *testing.T) {
	root := t.TempDir()
	// SQLite's path is a directory, so the real connection opening fails.
	app, err := openApplication(serverConfig{Database: databaseConfig{Path: root, Storage: root, Secret: "runtime-test", Readers: 2}, JobConcurrency: 1})
	if err == nil || app != nil {
		t.Fatalf("failed resource opening returned an application: %v %v", app, err)
	}
}
