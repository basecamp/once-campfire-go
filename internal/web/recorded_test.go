package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

func TestRecordedMessagesPreserveBodyAndInvalidate(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "recorded", "<p>one &amp; two</p>", "one & two")
	if err != nil {
		t.Fatal(err)
	}
	list := []database.Message{message}
	generation := app.DB.ContentGeneration()
	views, err := app.messageItems(ctx, list, generation)
	if err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := app.templates.ExecuteTemplate(&original, "messages", page{Messages: views}); err != nil {
		t.Fatal(err)
	}
	fragment, err := app.messageList(ctx, list, generation)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	first := httptest.NewRecorder()
	buffered := &responseBuffer{ResponseWriter: first}
	writeRecorded(buffered, 200, "before\x00marker\x00after", "\x00marker\x00", fragment)
	buffered.finish(request)
	if first.Body.String() != "before"+original.String()+"after" {
		t.Fatal("recorded rendering changed bytes")
	}
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	second := httptest.NewRecorder()
	buffered = &responseBuffer{ResponseWriter: second}
	writeRecorded(buffered, 200, "before\x00marker\x00after", "\x00marker\x00", fragment)
	buffered.finish(request)
	if second.Code != 304 || second.Body.Len() != 0 {
		t.Fatal("unchanged parts were not conditional", second.Code)
	}
	list[0].UpdatedAt = list[0].UpdatedAt.Add(time.Second)
	list[0].Body = "<p>changed</p>"
	changed, err := app.messageList(ctx, list, generation)
	if err != nil || !strings.Contains(string(changed.html), "changed") || changed.digest == fragment.digest {
		t.Fatal("stale message list", err)
	}
	if changed.digest != sha256.Sum256([]byte(changed.html)) {
		t.Fatal("incorrect cached digest")
	}
}
