package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func TestSearchMissRetainsQueryBodySnapshot(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, _ := app.DB.Rooms(ctx, user.ID)
	first, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("snapshot-first", "<p>snapshotneedle</p>"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("snapshot-second", "<p>snapshotneedle</p>"))
	if err != nil {
		t.Fatal(err)
	}
	// An edit changes both matching selection and the body returned on a miss.
	if _, err := app.DB.UpdateMessage(ctx, user.ID, first.ID, messageInput("", "<p>differentword</p>")); err != nil {
		t.Fatal(err)
	}
	body := func(part responsebody.Part) []byte {
		t.Helper()
		var b bytes.Buffer
		if _, err := part.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	var retained responsebody.Part
	for _, limit := range []int{0, 1, 32 << 20} {
		setFragmentLimit(app, limit)
		part, count, err := app.MessageQueries.Search(ctx, app.messageScope(ctx), user.ID, "snapshotneedle")
		if err != nil || count != 1 || bytes.Contains(body(part), []byte(fmt.Sprintf(`data-message-id="%d"`, first.ID))) || !bytes.Contains(body(part), []byte(fmt.Sprintf(`data-message-id="%d"`, second.ID))) {
			t.Fatalf("cache limit %d: stale selection/body, count %d / %v", limit, count, err)
		}
		retained, count, err = app.MessageQueries.Search(ctx, app.messageScope(ctx), user.ID, "snapshotneedle")
		if err != nil || count != 1 || !bytes.Equal(body(part), body(retained)) {
			t.Fatal("hit changed the selected body", err)
		}
	}
	// Eviction and a newer edit must not turn a captured Part into hydration.
	setFragmentLimit(app, 0)
	if _, err := app.DB.DeleteMessage(ctx, user.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/searches?q=snapshotneedle", nil)
	writer, r := app.withBrowserSession(w, app.normalizeRequest(r))
	app.respondPage(writer, r, "search", 200, page{User: user, Query: "snapshotneedle", SearchResultCount: 1, messageBody: &retained})
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), body(retained)) {
		t.Fatal("captured search body was lost after eviction/edit")
	}
	part, count, err := app.MessageQueries.Search(ctx, app.messageScope(ctx), user.ID, "snapshotneedle")
	if err != nil || count != 0 || part.Len() != 0 {
		t.Fatal("miss did not observe committed deletion", count, err)
	}
}

func TestSearchShowsFreshResultCount(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("count-first", "<p>countneedle</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, messageInput("count-second", "<p>countneedle</p>")); err != nil {
		t.Fatal(err)
	}
	counter := regexp.MustCompile(`(?s)class="searches__query[^"\n]*".*?<span class="flex-item-no-shrink">([0-9]+)</span>`)
	messageID := regexp.MustCompile(`data-message-id="[0-9]+"`)
	check := func(query string, want int) {
		t.Helper()
		response, body := perform(t, server, "GET", "/searches?q="+query, "", nil, cookie)
		match := counter.FindSubmatch(body)
		if ids := messageID.FindAll(body, -1); len(ids) != want {
			t.Fatalf("search %q rendered %d messages, want %d", query, len(ids), want)
		}
		if response.StatusCode != 200 || len(match) != 2 || string(match[1]) != strconv.Itoa(want) {
			t.Fatalf("search %q: status %d, badge %q, want %d", query, response.StatusCode, match, want)
		}
	}
	check("countneedle", 2)
	check("countneedle", 2) // A message-list cache hit must retain the count too.
	setFragmentLimit(app, 0)
	check("countneedle", 2) // Disabled retention uses the complete query directly.
	if _, err := app.DB.DeleteMessage(ctx, user.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	check("countneedle", 1)
	check("absentneedle", 0)
}
