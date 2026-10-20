package web

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestMediaRecordValidatorsSurviveFileResponses(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	source, err := os.Open("../../reference/reference/test/fixtures/files/moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	blob, err := app.Storage.Stage(ctx, "moon.jpg", "image/jpeg", source)
	if err != nil {
		t.Fatal(err)
	}
	account, err := app.DB.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bot, err := app.DB.CreateUser(ctx, user.ID, database.UserInput{Name: "Bot", Role: 2})
	if err != nil {
		t.Fatal(err)
	}
	avatar := "/users/" + app.Secrets.SignedID("User", user.ID, "avatar", time.Time{}) + "/avatar"
	botAvatar := "/users/" + app.Secrets.SignedID("User", bot.ID, "avatar", time.Time{}) + "/avatar"
	for _, item := range []struct {
		name, path string
		attach     func() error
	}{
		{"avatar", avatar, func() error {
			_, err := app.DB.UpdateUser(ctx, user.ID, user.ID, database.UserChanges{Avatar: &database.AttachmentInput{ID: blob.ID}})
			return err
		}},
		{"logo", "/account/logo", func() error {
			_, err := app.DB.UpdateAccount(ctx, user.ID, database.AccountInput{Logo: &database.AttachmentInput{ID: blob.ID}})
			return err
		}},
		{"bot", botAvatar, func() error {
			name := "Renamed bot"
			_, err := app.DB.UpdateBot(ctx, user.ID, bot.ID, database.UserChanges{Name: &name})
			return err
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			fetch := func(method, etag string) (*http.Response, []byte) {
				t.Helper()
				return storageResponse(t, server, method, item.path, http.Header{"Cookie": {cookie.String()}, "If-None-Match": {etag}})
			}
			first, _ := fetch("GET", "")
			etag := first.Header.Get("ETag")
			if first.StatusCode != 200 || etag == "" {
				t.Fatalf("stock response lacks record validator: %s %v", first.Status, first.Header)
			}
			// Distinct record versions are controlled without sleeping on clock precision.
			app.DB.Now = func() time.Time { return account.UpdatedAt.Add(time.Minute) }
			if err := item.attach(); err != nil {
				t.Fatal(err)
			}
			changed, body := fetch("GET", etag)
			etag = changed.Header.Get("ETag")
			if changed.StatusCode != 200 || len(body) == 0 || etag == "" || etag == first.Header.Get("ETag") {
				t.Fatalf("changed record has stale/missing validator: %s %v", changed.Status, changed.Header)
			}
			for _, method := range []string{"GET", "HEAD"} {
				conditional, body := fetch(method, etag)
				if conditional.StatusCode != 304 || len(body) != 0 || conditional.Header.Get("ETag") != etag {
					t.Fatalf("%s did not retain file validator: %s %v %q", method, conditional.Status, conditional.Header, body)
				}
			}
			if item.name == "avatar" {
				app.DB.Now = func() time.Time { return account.UpdatedAt.Add(2 * time.Minute) }
				if _, err := app.DB.DeleteAvatar(ctx, user.ID); err != nil {
					t.Fatal(err)
				}
				initials, body := fetch("GET", etag)
				if initials.StatusCode != 200 || initials.Header.Get("ETag") == etag || initials.Header.Get("Content-Type") != "image/svg+xml; charset=utf-8" {
					t.Fatalf("detached avatar stayed fresh: %s %v %s", initials.Status, initials.Header, string(body))
				}
			}
		})
	}
}
