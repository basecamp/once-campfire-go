package web

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// TestMessageControllersRenderFreshRecords (upstream, adopted): the message
// controllers render the current records after an edit — no recorded or
// fragment cache may serve a stale body.
func TestMessageControllersRenderFreshRecords(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "record-input", "<p>record before</p>", "record before")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateBoost(ctx, user.ID, message.ID, "record boost"); err != nil {
		t.Fatal(err)
	}
	check := func(body string) {
		t.Helper()
		for _, endpoint := range []struct{ path, content string }{
			{fmt.Sprintf("/rooms/%d/messages/%d", message.RoomID, message.ID), body},
			{fmt.Sprintf("/rooms/%d/messages/%d/edit", message.RoomID, message.ID), body},
			{fmt.Sprintf("/messages/%d/boosts", message.ID), "record boost"},
			{fmt.Sprintf("/messages/%d/boosts/new", message.ID), "new_boost_message_" + message.ClientID},
		} {
			response, data := perform(t, server, "GET", endpoint.path, "", nil, cookie)
			if response.StatusCode != 200 || !strings.Contains(string(data), endpoint.content) {
				t.Fatalf("%s: status %d, missing %q", endpoint.path, response.StatusCode, endpoint.content)
			}
		}
	}
	check("record before")
	if _, err := app.DB.UpdateMessage(ctx, user.ID, message.ID, "<p>record after</p>", "record after"); err != nil {
		t.Fatal(err)
	}
	check("record after")
}

// TestMessageItemsBatchMissesKeepOrderAndBytes (upstream, adopted): batched
// fragment misses must not shift the positions or bytes of the views,
// whether the inputs are full records or references.
func TestMessageItemsBatchMissesKeepOrderAndBytes(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	var records []database.Message
	for i := 0; i < 4; i++ {
		m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprint("batch-", i), fmt.Sprintf("<p>batch &amp; %d</p>", i), fmt.Sprintf("batch %d", i))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, m)
	}
	app.fragments = newFragmentCache(0)
	var want []template.HTML
	for _, record := range records {
		views, err := app.messageViews(ctx, []database.Message{record})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, views[0].Fragment)
	}
	for _, references := range []bool{false, true} {
		input := append([]database.Message(nil), records...)
		if references {
			for i, m := range input {
				input[i] = database.Message{ID: m.ID, RoomID: m.RoomID, UpdatedAt: m.UpdatedAt}
			}
		}
		for _, limit := range []int{0, 32 << 20} {
			app.fragments = newFragmentCache(limit)
			// Nonadjacent hits must not shift the positions of batched misses.
			if _, err := app.messageViews(ctx, []database.Message{records[0], records[2]}); err != nil {
				t.Fatal(err)
			}
			got, err := app.messageItems(ctx, input)
			if err != nil || len(got) != len(want) {
				t.Fatal("batch size/error differs", err)
			}
			for i := range want {
				if got[i].Fragment != want[i] {
					t.Fatalf("references=%v, limit=%d: message %d bytes/order differ", references, limit, i)
				}
			}
		}
	}
}

func TestRecordedMessagesPreserveBodyAndInvalidate(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "recorded", "<p>one &amp; two</p>", "one & two")
	if err != nil {
		t.Fatal(err)
	}
	list := []database.Message{message}
	fragment, err := app.messageList(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	original := fragment.html
	if string(original) == "" {
		t.Fatal("empty fragment")
	}
	// A second call with unchanged records must be served from the fragment
	// cache with the identical bytes.
	again, err := app.messageList(ctx, list)
	if err != nil || again.html != original {
		t.Fatal("cached message list changed bytes")
	}
	fragment, err = app.messageList(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	first := httptest.NewRecorder()
	buffered := &responseBuffer{ResponseWriter: first}
	writeRecorded(buffered, 200, "before\x00marker\x00after", "\x00marker\x00", fragment)
	buffered.finish(request)
	if first.Body.String() != "before"+string(original)+"after" {
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
	changed, err := app.messageList(ctx, list)
	if err != nil || !strings.Contains(string(changed.html), "changed") || changed.digest == fragment.digest {
		t.Fatal("stale message list", err)
	}
	if changed.digest != sha256.Sum256([]byte(changed.html)) {
		t.Fatal("incorrect cached digest")
	}
}
