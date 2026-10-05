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
	app, err := web.New(db, secrets, os.Getenv("DISABLE_SSL") == "", storage)
	if err != nil {
		return err
	}
	defer app.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Encoding composition (engine design §3.2): the engine wraps the legacy
	// handler, which keeps the exact bodyLimit(Deflate(app)) chain, and
	// front.Serve skips its own Deflate wrap. Engine-owned routes encode
	// themselves; fallback routes stay byte-identical to the pre-engine chain.
	mode := engine.ParseMode(os.Getenv("CAMPFIRE_ENGINE"))
	slog.Info("engine", "mode", mode)
	root := engine.New(front.Deflate(app), engine.Config{Mode: mode})
	config := front.FromEnv()
	config.SkipDeflate = true
	return front.Serve(ctx, config, root)
}
