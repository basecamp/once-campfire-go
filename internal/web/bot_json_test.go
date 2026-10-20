package web

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestBotJSONValidatesParamsAndPreservesRawBodySelection(t *testing.T) {
	app, server, cookie, owner := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d/api/messages", rooms[0].ID)
	for _, raw := range []string{`{"incomplete":`, `{} {}`, " \r\n"} {
		var before, after int
		if err := app.DB.Read.QueryRow("SELECT count(*) FROM messages").Scan(&before); err != nil {
			t.Fatal(err)
		}
		response, _ := perform(t, server, "POST", path, "application/json", strings.NewReader(raw), cookie)
		if err := app.DB.Read.QueryRow("SELECT count(*) FROM messages").Scan(&after); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 400 || before != after {
			t.Errorf("invalid JSON %q: status %d, messages %d -> %d", raw, response.StatusCode, before, after)
		}
	}
	response, _ := perform(t, server, "POST", path+"?attachment=", "application/x-www-form-urlencoded", strings.NewReader(""), cookie)
	if response.StatusCode != 422 {
		t.Errorf("empty body/attachment bypassed presence check: %s", response.Status)
	}
	const initial = `{"text":"literaljsonneedle"}`
	response, _ = perform(t, server, "POST", path, "application/json", strings.NewReader(initial), cookie)
	if response.StatusCode != 201 {
		t.Fatalf("valid JSON raw create: %s", response.Status)
	}
	location := response.Header.Get("Location")
	id, err := strconv.ParseInt(location[strings.LastIndexByte(location, '/')+1:], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.Message(ctx, id)
	if err != nil || message.Body != initial {
		t.Fatalf("raw JSON body: %q, %v", message.Body, err)
	}
	update := path + "/" + strconv.FormatInt(id, 10)
	for _, test := range []struct {
		raw    string
		status int
		body   string
		hits   int
	}{
		{`{"incomplete":`, 400, initial, 1},
		{" \n", 400, initial, 1},
		{`{"attachment":"invalid"}`, 500, initial, 1},
		{`{"attachment":"","":"must not become body"}`, 200, initial, 1},
		{`{"attachment":null}`, 200, `{"attachment":null}`, 0},
	} {
		response, _ := perform(t, server, "PUT", update, "application/json", strings.NewReader(test.raw), cookie)
		if response.StatusCode != test.status {
			t.Errorf("%q: status %d, want %d", test.raw, response.StatusCode, test.status)
		}
		message, err := app.DB.Message(ctx, id)
		if err != nil || message.Body != test.body {
			t.Errorf("%q: stored body %q, want %q: %v", test.raw, message.Body, test.body, err)
		}
		hits, err := app.DB.SearchReferences(ctx, owner.ID, "literaljsonneedle")
		if err != nil || len(hits) != test.hits {
			t.Errorf("%q: FTS hits %d, want %d: %v", test.raw, len(hits), test.hits, err)
		}
	}
	// Null selects raw-body attributes, so an existing attachment must survive.
	contentType := "text/plain"
	blob, err := app.DB.CreateBlob(ctx, database.Blob{Filename: "retained.txt", ContentType: &contentType, ByteSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.DB.UpdateMessage(ctx, owner.ID, id, database.MessageInput{Attachment: &blob.ID}); err != nil {
		t.Fatal(err)
	}
	response, _ = perform(t, server, "PUT", update, "application/json", strings.NewReader(`{"attachment":null}`), cookie)
	var attached int
	if err = app.DB.Read.QueryRow("SELECT count(*) FROM active_storage_attachments WHERE record_type='Message' AND record_id=? AND blob_id=?", id, blob.ID).Scan(&attached); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || attached != 1 {
		t.Errorf("null attachment changed association: %s, count %d", response.Status, attached)
	}

	response, _ = perform(t, server, "POST", path, "application/json", strings.NewReader(`{"attachment":""}`), cookie)
	if response.StatusCode != 201 {
		t.Fatalf("empty attachment create: %s", response.Status)
	}
	location = response.Header.Get("Location")
	id, err = strconv.ParseInt(location[strings.LastIndexByte(location, '/')+1:], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	message, err = app.DB.Message(ctx, id)
	if err != nil || message.Body != "" {
		t.Errorf("empty attachment selected raw body %q: %v", message.Body, err)
	}
}
