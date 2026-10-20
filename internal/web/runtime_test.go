package web

import (
	"context"
	"log/slog"
	"os"
	"strconv"
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
)

// Private HTTP tests own their concrete resources. They do not import the CLI
// composition root, and persistence stays open for assertions after shutdown.
type testRuntime struct {
	*Server
	Jobs           *jobs.Runner
	ContentQueries *application.ContentQueries
	WebhookReplies *application.WebhookReplies
	closeOnce      sync.Once
}

func newTestRuntime(db *database.DB, secrets *rails.Secrets, root string) (*testRuntime, error) {
	presenter, err := presentation.NewRenderer(secrets)
	if err != nil {
		return nil, err
	}
	fragmentBytes, responseBytes, concurrency := 32<<20, 64<<20, 2
	if raw, ok := os.LookupEnv("CAMPFIRE_FRAGMENT_CACHE_MB"); ok {
		mb, err := strconv.Atoi(raw)
		if err != nil {
			return nil, err
		}
		fragmentBytes = mb << 20
	}
	if raw := os.Getenv("CAMPFIRE_RESPONSE_CACHE_MB"); raw != "" {
		mb, err := strconv.Atoi(raw)
		if err != nil {
			return nil, err
		}
		responseBytes = mb << 20
	}
	if n, _ := strconv.Atoi(os.Getenv("JOB_CONCURRENCY")); n > 0 {
		concurrency = n
	}
	var vapid *integrations.VAPID
	if public, private := os.Getenv("VAPID_PUBLIC_KEY"), os.Getenv("VAPID_PRIVATE_KEY"); public != "" && private != "" {
		vapid, err = integrations.NewVAPID("https://github.com/basecamp/once-campfire-go", public, private)
		if err != nil {
			slog.Error("Web Push disabled", "error", err)
		}
	}
	hub := cable.New(db, secrets)
	runner := jobs.New(concurrency, "push", "webhook", "purge", "ban", "analyze")
	store := storage.New(db, secrets, root)
	push := integrations.NewPushSender(vapid)
	fragments := presentation.NewFragments(presenter, fragmentBytes)
	cleanup := &application.Cleanup{Storage: store, Jobs: runner}
	content := &application.ContentQueries{DB: db, Secrets: secrets}
	notificationQueries := &application.NotificationQueries{DB: db, Content: content}
	messages := &application.MessageQueries{DB: db, Presentation: presenter, Content: content, Fragments: fragments}
	commands := &application.Messages{DB: db, Storage: store, Jobs: runner, Cleanup: cleanup}
	publications := &application.MessagePublications{Queries: messages, Cable: hub}
	notifications := &application.MessageNotifications{DB: db, Queries: notificationQueries, Cable: hub, Push: push, Jobs: runner}
	webhooks := &application.WebhookReplies{Queries: notificationQueries, Client: integrations.NewWebhookClient(), Storage: store, Commands: commands, Notifications: notifications, Publications: publications, Jobs: runner}
	attachments := &application.Attachments{DB: db, Storage: store, Jobs: runner, Cleanup: cleanup}
	http := New(Dependencies{
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
	}, Config{ResponseCacheBytes: responseBytes})
	return &testRuntime{Server: http, Jobs: runner, ContentQueries: content, WebhookReplies: webhooks}, nil
}

func (app *testRuntime) Close() {
	app.closeOnce.Do(func() {
		app.StopIntake()
		app.Cable.Close()
		app.WaitHandlers()
		app.Jobs.Close(10 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Storage.RetryPurges(ctx); err != nil {
			slog.Error("shutdown purge continuation failed", "error", err)
		}
	})
}
