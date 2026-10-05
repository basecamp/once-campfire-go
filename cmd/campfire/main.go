package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"syscall"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/web"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		slog.Error("campfire", "error", err)
		os.Exit(1)
	}
}

// defaultGCPercent is the GC target when GOGC isn't set: the collector runs half as often as at
// Go's default of 100, for roughly a quarter more heap (bench/results/apples-step4-*).
const defaultGCPercent = 200

func run() error {
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(defaultGCPercent)
	}
	if path := os.Getenv("GO_CPU_PROFILE"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := pprof.StartCPUProfile(file); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}

	command := "server"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command != "server" && command != "db:prepare" && command != "backup" {
		return fmt.Errorf("unknown command %q (server, db:prepare, or backup)", command)
	}
	secrets, err := rails.NewSecrets(os.Getenv("SECRET_KEY_BASE"))
	if err != nil {
		return err
	}
	storage := env("CAMPFIRE_STORAGE_PATH", "storage")
	path := env("CAMPFIRE_DATABASE_PATH", filepath.Join(storage, "db", env("RAILS_ENV", "production")+".sqlite3"))
	// One reader connection per RAILS_MAX_THREADS (default 5), as the reference's db_readers.
	readers, err := strconv.Atoi(env("RAILS_MAX_THREADS", "5"))
	if err != nil {
		return fmt.Errorf("RAILS_MAX_THREADS: %w", err)
	}
	db, err := database.Open(path, max(1, readers))
	if err != nil {
		return err
	}
	defer db.Close()
	if command == "backup" {
		return db.Backup(context.Background(), filepath.Join(storage, "backups", filepath.Base(path)))
	}
	if command == "db:prepare" {
		return nil
	}
	app, err := web.New(db, secrets, os.Getenv("DISABLE_SSL") == "", storage)
	if err != nil {
		return err
	}
	defer app.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return front.Serve(ctx, front.FromEnv(), app)
}
