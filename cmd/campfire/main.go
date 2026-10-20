package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
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
	if command != "server" && command != "db:prepare" && command != "backup" && command != "restore" {
		return fmt.Errorf("unknown command %q (server, db:prepare, backup, or restore)", command)
	}
	databaseConfig := databaseConfigFromEnv()
	if command == "restore" {
		return database.Restore(context.Background(), databaseConfig.backupPath(), databaseConfig.Path)
	}
	if command != "server" {
		if _, err := rails.NewSecrets(databaseConfig.Secret); err != nil {
			return err
		}
		db, err := database.Open(databaseConfig.Path, databaseConfig.Readers)
		if err != nil {
			return err
		}
		if command == "db:prepare" {
			return db.Close()
		}
		defer db.Close()
		return db.Backup(context.Background(), databaseConfig.backupPath())
	}
	config, err := serverConfigFromEnv(databaseConfig)
	if err != nil {
		return err
	}
	app, err := openApplication(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	closing, finished := make(chan struct{}), make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-closing:
		case <-finished:
			return
		}
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			// Native media work cannot always be preempted. This is forced exit,
			// not successful teardown: persistence stays open under surviving work.
			slog.Error("shutdown grace expired; forcing process exit")
			os.Exit(1)
		}
	}()
	defer func() {
		close(closing)
		if err := app.Close(); err != nil {
			slog.Error("database shutdown failed", "error", err)
		}
		close(finished)
	}()
	return front.Serve(ctx, front.FromEnv(), app.HTTP)
}
