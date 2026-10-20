package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestAccountLogoURLsTrackReplacementAndDeletion(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	ctx := context.Background()
	check := func() (string, []byte) {
		t.Helper()
		account, err := app.DB.Account(ctx)
		if err != nil {
			t.Fatal(err)
		}
		path := "/account/logo?v=" + account.UpdatedAt.UTC().Format("20060102150405")
		for _, name := range []string{"join", "account", "room-invitation"} {
			body, err := app.Presentation.Markup(name, page{Account: account, User: database.User{Role: 0}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, `src="`+path+`"`) {
				t.Errorf("%s: logo URL does not track account version %s", name, path)
			}
		}
		response, body := perform(t, server, "GET", path, "", nil, nil)
		if response.StatusCode != http.StatusOK ||
			!strings.Contains(response.Header.Get("Cache-Control"), "max-age=300") {
			t.Fatalf("cacheable logo: %s %v", response.Status, response.Header)
		}
		return path, body
	}
	beforePath, before := check()
	account, err := app.DB.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := account.UpdatedAt.Add(time.Second)
	app.DB.Now = func() time.Time { return now }
	fixture, err := os.ReadFile("../../reference/reference/test/fixtures/files/moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	file, err := form.CreateFormFile("account[logo]", "moon.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(fixture); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	response, body := perform(
		t,
		server,
		"PATCH",
		"/account",
		form.FormDataContentType(),
		&payload,
		cookie,
	)
	if response.StatusCode != http.StatusFound {
		t.Fatalf("replace logo: %s %s", response.Status, body)
	}
	afterPath, after := check()
	if afterPath == beforePath || bytes.Equal(before, after) {
		t.Fatal("replacement did not invalidate the cacheable logo URL and bytes")
	}
	// Both mutations occur within the old response's five-minute freshness window.
	now = now.Add(time.Second)
	response, body = perform(t, server, "DELETE", "/account/logo", "", nil, cookie)
	if response.StatusCode != http.StatusFound {
		t.Fatalf("delete logo: %s %s", response.Status, body)
	}
	deletedPath, deleted := check()
	if deletedPath == afterPath || !bytes.Equal(deleted, before) {
		t.Fatal("deletion did not select a fresh URL for the fallback logo")
	}
}

func TestAccountSettingBooleanCasting(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	field := "account[settings][restrict_room_creation_to_administrators]"
	for _, tc := range []struct {
		value string
		want  any
	}{
		{"1", true},
		{"off", false},
		{"OFF", false},
		{"f", false},
		{"F", false},
		{"FALSE", false},
		{"false", false},
		{"0", false},
		{"", nil},
	} {
		response, body := perform(
			t,
			server,
			"PATCH",
			"/account",
			"application/x-www-form-urlencoded",
			strings.NewReader(url.Values{field: {tc.value}}.Encode()),
			cookie,
		)
		if response.StatusCode != http.StatusFound {
			t.Fatalf("value %q: %s %s", tc.value, response.Status, body)
		}
		account, err := app.DB.Account(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var settings map[string]any
		if err := json.Unmarshal(account.Settings, &settings); err != nil {
			t.Fatal(err)
		}
		if settings["restrict_room_creation_to_administrators"] != tc.want {
			t.Errorf("value %q: settings %s, want %v", tc.value, account.Settings, tc.want)
		}
	}
}

func TestCustomStylesSaveReturnsToEditor(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Client().Jar = jar
	styles := ":root { --contract-color: red; }"
	for _, tc := range []struct{ method, field, value string }{
		{"PATCH", "account[custom_styles]", styles},
		{"PUT", "account[ignored]", "value"},
	} {
		response, body := perform(
			t,
			server,
			tc.method,
			"/account/custom_styles",
			"application/x-www-form-urlencoded",
			strings.NewReader(url.Values{tc.field: {tc.value}}.Encode()),
			cookie,
		)
		if response.StatusCode != http.StatusFound ||
			response.Header.Get("Location") != "/account/custom_styles/edit" {
			t.Errorf("save %s: %s %s", tc.field, response.Status, response.Header.Get("Location"))
		}
		account, err := app.DB.Account(context.Background())
		if err != nil || account.CustomStyles != styles {
			t.Errorf("save %s changed styles: %q, %v", tc.field, account.CustomStyles, err)
		}
		response, body = perform(t, server, "GET", "/account/custom_styles/edit", "", nil, cookie)
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), styles) ||
			!strings.Contains(string(body), "✓") {
			t.Errorf("save %s: editor missing styles or success notice", tc.field)
		}
	}
}
