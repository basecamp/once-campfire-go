package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/presentation"
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func capturedMessagePart(ctx context.Context, app *testRuntime, records []database.Message) (responsebody.Part, error) {
	views, err := app.MessageQueries.Views(ctx, app.presentationFacts(ctx), records)
	if err != nil {
		return responsebody.Part{}, err
	}
	fragments := make([]template.HTML, len(views))
	for i, view := range views {
		fragments[i] = view.Fragment
	}
	return presentation.FragmentList(fragments), nil
}

func TestMessageControllersRenderFreshRecords(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("record-input", "<p>record before</p>"))
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
	if _, err := app.DB.UpdateMessage(ctx, user.ID, message.ID, messageInput("", "<p>record after</p>")); err != nil {
		t.Fatal(err)
	}
	check("record after")
}

func TestScopedMessageMissesKeepOrderAndBytes(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	var records []database.Message
	for i := 0; i < 4; i++ {
		m, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput(fmt.Sprint("batch-", i), fmt.Sprintf("<p>batch &amp; %d</p>", i)))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, m.Message)
	}
	setFragmentLimit(app, 0)
	var want []template.HTML
	for _, record := range records {
		views, err := app.MessageQueries.Views(ctx, app.presentationFacts(ctx), []database.Message{record})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, views[0].Fragment)
	}
	for _, limit := range []int{0, 32 << 20} {
		setFragmentLimit(app, limit)
		// Nonadjacent fragment hits must not shift the positions of scoped misses.
		for _, warm := range []struct {
			anchor    int64
			direction string
		}{
			{records[1].ID, "before"}, // first fragment
			{records[1].ID, "after"},  // nonadjacent third (and fourth) fragments
		} {
			if _, _, err := app.MessageQueries.Page(ctx, app.messageScope(ctx), user.ID, rooms[0].ID, warm.anchor, warm.direction, false); err != nil {
				t.Fatal(err)
			}
		}
		got, err := app.MessageQueries.Views(ctx, app.presentationFacts(ctx), records)
		if err != nil || len(got) != len(want) {
			t.Fatal("batch size/error differs", err)
		}
		for i := range want {
			if got[i].Fragment != want[i] {
				t.Fatalf("limit=%d: message %d bytes/order differ", limit, i)
			}
		}
		part, count, err := app.MessageQueries.Page(ctx, app.messageScope(ctx), user.ID, rooms[0].ID, 0, "around", false)
		var body bytes.Buffer
		if err != nil || count != len(want) {
			t.Fatal("scoped batch size/error differs", count, err)
		}
		if _, err := part.WriteTo(&body); err != nil {
			t.Fatal(err)
		}
		var expected strings.Builder
		for _, fragment := range want {
			expected.WriteString(string(fragment))
		}
		if body.String() != expected.String() {
			t.Fatalf("limit=%d: scoped batch changed bytes/order", limit)
		}
	}

}

func TestRecordedMessagesPreserveBodyAndInvalidate(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("recorded", "<p>one &amp; two</p>"))
	if err != nil {
		t.Fatal(err)
	}
	list := []database.Message{message.Message}
	views, err := app.MessageQueries.Views(ctx, app.presentationFacts(ctx), list)
	if err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := app.Presentation.ExecuteTemplate(&original, "messages", page{Messages: views}); err != nil {
		t.Fatal(err)
	}
	fragment, err := capturedMessagePart(ctx, app, list)
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
	// Keep the existing wire validator even though compression now shares its
	// full strong part identity. This is the pre-optimization construction.
	hash := sha256.New()
	for _, value := range []string{"before", original.String(), "after"} {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
		hash.Write(size[:])
		digest := sha256.Sum256([]byte(value))
		hash.Write(digest[:])
	}
	if first.Header().Get("ETag") != fmt.Sprintf("W/\"%x\"", hash.Sum(nil)[:16]) {
		t.Fatal("part identity changed the wire validator")
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
	changed, err := capturedMessagePart(ctx, app, list)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if _, err := changed.WriteTo(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), "changed") || changed.Digest() == fragment.Digest() {
		t.Fatal("stale message list")
	}
	if changed.Digest() != sha256.Sum256(body.Bytes()) {
		t.Fatal("incorrect cached digest")
	}
}
