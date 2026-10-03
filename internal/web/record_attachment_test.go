package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/basecamp/once-campfire-go/internal/storage"
	"strings"
	"testing"
)

func TestProfileAttachmentAssignments(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	contentType := "image/png"
	blob, err := app.Storage.Create(ctx, storage.Blob{Filename: "avatar.png", ContentType: &contentType})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Storage.Attach(ctx, blob, "User", user.ID, "avatar"); err != nil {
		t.Fatal(err)
	}
	response, _ := perform(t, server, "PATCH", "/users/me/profile", "application/json", strings.NewReader(`{"user":{"avatar":null,"bio":"kept"}}`), cookie)
	if response.StatusCode != 302 {
		t.Fatal(response.Status)
	}
	if _, err = app.Storage.Attached(ctx, "User", user.ID, "avatar"); err != nil {
		t.Fatal("nil profile avatar must be omitted", err)
	}
	response, _ = perform(t, server, "PATCH", "/users/me/profile", "application/json", strings.NewReader(`{"user":{"avatar":"invalid","bio":"rollback"}}`), cookie)
	if response.StatusCode != 500 {
		t.Fatal(response.Status)
	}
	updated, err := app.DB.User(ctx, user.ID)
	if err != nil || updated.Bio != "kept" {
		t.Fatalf("invalid attachment changed user: %+v %v", updated, err)
	}
	response, _ = perform(t, server, "PATCH", "/users/me/profile", "application/json", strings.NewReader(`{"user":{"avatar":""}}`), cookie)
	if response.StatusCode != 302 {
		t.Fatal(response.Status)
	}
	if _, err = app.Storage.Attached(ctx, "User", user.ID, "avatar"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("empty string must delete", err)
	}
}
func TestBotPartialUpdateKeepsOmittedFields(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	webhook := "https://example.test/hook"
	bot, err := app.DB.CreateUser(context.Background(), "Notifier", "", "", "", 2, &webhook)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := perform(t, server, "PATCH", fmt.Sprintf("/account/bots/%d", bot.ID), "application/json", strings.NewReader(`{"user":{"name":"Renamed"}}`), cookie)
	if response.StatusCode != 302 {
		t.Fatal(response.Status)
	}
	var actual string
	if err = app.DB.Read.QueryRow("SELECT url FROM webhooks WHERE user_id=?", bot.ID).Scan(&actual); err != nil || actual != webhook {
		t.Fatalf("omitted webhook changed: %q %v", actual, err)
	}
	response, _ = perform(t, server, "PATCH", fmt.Sprintf("/account/bots/%d", bot.ID), "application/json", strings.NewReader(`{"user":{"webhook_url":""}}`), cookie)
	if response.StatusCode != 302 {
		t.Fatal(response.Status)
	}
	bot, err = app.DB.User(context.Background(), bot.ID)
	if err != nil || bot.Name != "Renamed" {
		t.Fatalf("omitted name changed: %+v %v", bot, err)
	}
}
