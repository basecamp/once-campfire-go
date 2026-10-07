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
	first, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "snapshot-first", "<p>snapshotneedle</p>", "snapshotneedle")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "snapshot-second", "<p>snapshotneedle</p>", "snapshotneedle")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := app.DB.SearchReferences(ctx, user.ID, "snapshotneedle")
	if err != nil {
		t.Fatal(err)
	}
	// A real commit between reference selection and resolving the message body.
	if _, err := app.DB.UpdateMessage(ctx, user.ID, first.ID, "<p>differentword</p>", "differentword"); err != nil {
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
		app.fragments = newFragmentCache(limit)
		part, count, err := app.searchMessageList(ctx, user.ID, "snapshotneedle", refs)
		if err != nil || count != 1 || bytes.Contains(body(part), []byte(fmt.Sprintf(`data-message-id="%d"`, first.ID))) || !bytes.Contains(body(part), []byte(fmt.Sprintf(`data-message-id="%d"`, second.ID))) {
			t.Fatalf("cache limit %d: stale selection/body, count %d / %v", limit, count, err)
		}
		fresh, err := app.DB.SearchReferences(ctx, user.ID, "snapshotneedle")
		if err != nil {
			t.Fatal(err)
		}
		retained, count, err = app.searchMessageList(ctx, user.ID, "snapshotneedle", fresh)
		if err != nil || count != 1 || !bytes.Equal(body(part), body(retained)) {
			t.Fatal("hit changed the selected body", err)
		}
	}
	// Eviction and a newer edit must not turn a captured Part into hydration.
	app.fragments = newFragmentCache(0)
	if err := app.DB.DeleteMessage(ctx, user.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/searches?q=snapshotneedle", nil)
	writer, r := app.withBrowserSession(w, r)
	app.render(writer, r, "search", 200, page{User: user, Query: "snapshotneedle", SearchResultCount: 1, messageBody: &retained})
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), body(retained)) {
		t.Fatal("captured search body was lost after eviction/edit")
	}
	part, count, err := app.searchMessageList(ctx, user.ID, "snapshotneedle", refs)
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
	first, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "count-first", "<p>countneedle</p>", "countneedle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "count-second", "<p>countneedle</p>", "countneedle"); err != nil {
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
	app.fragments = newFragmentCache(0)
	check("countneedle", 2) // Disabled retention uses the complete query directly.
	if err := app.DB.DeleteMessage(ctx, user.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	check("countneedle", 1)
	check("absentneedle", 0)
}
