package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"syscall"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/engine"
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
func run() error {
	// GC policy from the environment (gc.go) must be in effect before any
	// serving work starts: apply now, and the effective settings land in the
	// startup log either way.
	applyGCPolicy(os.LookupEnv)

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
	db, err := database.Open(path, max(1, runtime.GOMAXPROCS(0)))
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
	app, err := web.New(db, secrets, os.Getenv("DISABLE_SSL") == "", path, storage)
	if err != nil {
		return err
	}
	defer app.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Encoding composition (engine design §3.2) lives in compose.go: the
	// engine wraps the legacy front.Deflate chain, and rootConfig marks the
	// front configuration precomposed so front.Serve does not add a second
	// encoding layer.
	setting := os.Getenv("CAMPFIRE_ENGINE")
	mode, ok := engine.ParseMode(setting)
	if !ok {
		slog.Warn("engine: unrecognized CAMPFIRE_ENGINE value, defaulting to on", "value", setting)
	}
	slog.Info("engine", "mode", mode)
	return front.Serve(ctx, rootConfig(front.FromEnv()), buildRoot(app, mode))
}
