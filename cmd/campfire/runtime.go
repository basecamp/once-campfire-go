package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/basecamp/once-campfire-go/internal/application"
	"github.com/basecamp/once-campfire-go/internal/cable"
	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"github.com/basecamp/once-campfire-go/internal/web"
)

// The CLI owns resource lifetimes, not the HTTP adapter or application services.
type applicationRuntime struct {
	HTTP      *web.Server
	db        *database.DB
	cable     *cable.Hub
	jobs      *jobs.Runner
	storage   *storage.Store
	closeOnce sync.Once
	closeErr  error
}

func openApplication(config serverConfig) (*applicationRuntime, error) {
	// Complete fallible, immutable preparation before starting workers. Open
	// unwinds its own partial SQLite connections; no fallible construction follows.
	secrets, err := rails.NewSecrets(config.Database.Secret)
	if err != nil {
		return nil, err
	}
	presenter, err := presentation.NewRenderer(secrets)
	if err != nil {
		return nil, err
	}
	var vapid *integrations.VAPID
	if config.PushPublic != "" && config.PushPrivate != "" {
		vapid, err = integrations.NewVAPID(config.PushSubject, config.PushPublic, config.PushPrivate)
		if err != nil {
			slog.Error("Web Push disabled", "error", err)
		}
	}
	db, err := database.Open(config.Database.Path, config.Database.Readers)
	if err != nil {
		return nil, err
	}
	hub := cable.New(db, secrets)
	runner := jobs.New(config.JobConcurrency, "push", "webhook", "purge", "ban", "analyze")
	store := storage.New(db, secrets, config.Database.Storage)
	push := integrations.NewPushSender(vapid)
	fragments := presentation.NewFragments(presenter, config.FragmentBytes)
	cleanup := &application.Cleanup{Storage: store, Jobs: runner}
	content := &application.ContentQueries{DB: db, Secrets: secrets}
	notificationQueries := &application.NotificationQueries{DB: db, Content: content}
	messages := &application.MessageQueries{DB: db, Presentation: presenter, Content: content, Fragments: fragments}
	commands := &application.Messages{DB: db, Storage: store, Jobs: runner, Cleanup: cleanup}
	publications := &application.MessagePublications{Queries: messages, Cable: hub}
	notifications := &application.MessageNotifications{DB: db, Queries: notificationQueries, Cable: hub, Push: push, Jobs: runner}
	webhooks := &application.WebhookReplies{Queries: notificationQueries, Client: integrations.NewWebhookClient(), Storage: store, Commands: commands, Notifications: notifications, Publications: publications, Jobs: runner}
	attachments := &application.Attachments{DB: db, Storage: store, Jobs: runner, Cleanup: cleanup}
	http := web.New(web.Dependencies{
		DB: db, Secrets: secrets, Cable: hub, Storage: store, Push: push,
		Unfurler: integrations.NewUnfurler(), Presentation: presenter, Fragments: fragments,
		MessageQueries: messages, MessageCommands: commands,
		MessagePublications: publications,
		BoostCommands:       &application.Boosts{DB: db, Presentation: presenter, Cable: hub},
		MessageEffects:      &application.MessageEffects{Notifications: notifications, Webhooks: webhooks, Publications: publications},
		NotificationQueries: notificationQueries,
		PageQueries:         &application.PageQueries{DB: db},
		BotQueries:          &application.BotQueries{DB: db},
		RoomQueries:         &application.RoomQueries{DB: db},
		RoomPublications:    &application.RoomPublications{DB: db, Presentation: presenter, Cable: hub},
		AccountQueries:      &application.AccountQueries{DB: db, Secrets: secrets},
		Searches:            &application.Searches{DB: db, Messages: messages},
		RoomCommands:        &application.Rooms{DB: db, Cable: hub, Cleanup: cleanup},
		AccountCommands:     &application.Accounts{DB: db, Attachments: attachments, Messages: commands, Publications: publications, Cable: hub, Jobs: runner},
		SessionCommands:     &application.Sessions{DB: db, Cable: hub},
	}, web.Config{Secure: config.Secure, ResponseCacheBytes: config.ResponseBytes})
	return &applicationRuntime{HTTP: http, db: db, cable: hub, jobs: runner, storage: store}, nil
}

func (app *applicationRuntime) Close() error {
	app.closeOnce.Do(func() {
		app.HTTP.StopIntake()
		// Upgraded sockets and admitted commands can still submit dependent work.
		app.cable.Close()
		app.HTTP.WaitHandlers()
		app.jobs.Close(10 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.storage.RetryPurges(ctx); err != nil {
			slog.Error("shutdown purge continuation failed", "error", err)
		}
		app.closeErr = app.db.Close()
	})
	return app.closeErr
}
