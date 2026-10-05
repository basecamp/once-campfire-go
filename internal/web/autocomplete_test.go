package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestMentionAutocompleteAndRendering(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	_, err := app.DB.CreateUser(ctx, "Other <person>", "other@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, raw := perform(t, server, "GET", "/autocompletable/users.json?query=Owner", "", nil, cookie)
	if response.StatusCode != 200 || response.Header.Get("X-Total-Count") != "1" {
		t.Fatalf("%d %s", response.StatusCode, raw)
	}
	var users []struct{ SGID string }
	if err = json.Unmarshal(raw, &users); err != nil || len(users) != 1 {
		t.Fatal(string(raw))
	}
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	body := fmt.Sprintf(`<p>Hi <action-text-attachment sgid="%s"></action-text-attachment></p>`, users[0].SGID)
	form := url.Values{"message[body]": {body}}
	response, raw = perform(t, server, "POST", fmt.Sprintf("/rooms/%d/messages", room.ID), "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), cookie)
	if response.StatusCode != 200 {
		t.Fatalf("%d %s", response.StatusCode, raw)
	}
	messages, err := app.DB.Messages(ctx, room.ID, 0)
	if err != nil || len(messages) != 1 {
		t.Fatal(err, messages)
	}
	response, raw = perform(t, server, "GET", fmt.Sprintf("/rooms/%d/messages/%d", room.ID, messages[0].ID), "", nil, cookie)
	if response.StatusCode != 200 || !strings.Contains(string(raw), `class="mention"`) {
		t.Fatalf("%d %s", response.StatusCode, raw)
	}
	plain, mentioned := app.plainText(ctx, messages[0].Body), app.mentionedIDs(ctx, messages[0].Body)
	if plain != "Hi @Owner" || len(mentioned) != 1 || mentioned[0] != owner.ID {
		t.Fatal(plain, mentioned)
	}
	response, raw = perform(t, server, "GET", "/autocompletable/users?filter=Other", "", nil, cookie)
	if response.StatusCode != 200 || !strings.Contains(string(raw), `<lexxy-prompt-item`) || strings.Contains(string(raw), `Other <person>`) {
		t.Fatalf("%d %s", response.StatusCode, raw)
	}
	response, _ = perform(t, server, "GET", "/autocompletable/users?room_id=9999", "", nil, cookie)
	if response.StatusCode != 404 {
		t.Fatal(response.StatusCode)
	}
}
func TestManifestAndServiceWorker(t *testing.T) {
	app, server, _, _ := testApp(t)
	ctx := context.Background()
	name := `A "quoted" \\ name`
	account, _, err := app.DB.AccountFirst(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AccountUpdate(ctx, account, &name, nil, nil); err != nil {
		t.Fatal(err)
	}
	response, raw := perform(t, server, "GET", "/webmanifest.json", "", nil, nil)
	var manifest struct {
		Name  string
		Icons []struct{ Src string }
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err, string(raw))
	}
	if response.StatusCode != 200 || manifest.Name != name || len(manifest.Icons) != 3 || strings.Contains(manifest.Icons[0].Src, "&amp;") {
		t.Fatal(string(raw))
	}
	response, raw = perform(t, server, "GET", "/service-worker.js", "", nil, nil)
	if response.StatusCode != 200 || !strings.Contains(string(raw), `self.addEventListener("push"`) {
		t.Fatal(response.StatusCode, string(raw))
	}
}
