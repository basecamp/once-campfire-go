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
	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

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

func TestMessageListOwnershipAndAdmission(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "ownership", "<p>owned bytes</p>", "owned bytes")
	if err != nil {
		t.Fatal(err)
	}
	messages := []database.Message{message}
	original, err := app.messageList(ctx, messages)
	if err != nil {
		t.Fatal(err)
	}
	body := func(part responsebody.Part) string {
		t.Helper()
		var b bytes.Buffer
		if _, err := part.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	want := body(original)
	key := app.fragmentKey(context.Background(), messageListCacheKey(messages))
	cost := len(key) + len(want) + 240
	for _, test := range []struct {
		name     string
		limit    int
		retained bool
	}{
		{"disabled", 0, false},
		{"oversized", (cost - 1) * 4, false},
		{"admitted", cost * 4, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			app.fragments = newFragmentCache(test.limit)
			part, err := app.messageList(ctx, messages)
			if err != nil || body(part) != want || part.Digest() != original.Digest() {
				t.Fatal("cache admission changed response", err)
			}
			entry, retained := app.fragments.entry(key)
			if retained != test.retained {
				t.Fatalf("retained=%v, want %v", retained, test.retained)
			}
			if retained && (entry.html != "" || entry.bytes != cost || body(entry.part) != want) {
				t.Fatal("list payload must be retained and charged once")
			}
			// Eviction releases the cache's reference, not an outstanding response.
			for i := 0; i < 40; i++ {
				app.fragments.putEntry(fragmentEntry{key: fmt.Sprint(i), part: responsebody.NewPart([]byte(want))})
			}
			if _, retained := app.fragments.entry(key); retained || app.fragments.bytes > test.limit {
				t.Fatal("eviction/admission bound was not enforced")
			}
			if body(part) != want || body(original) != want {
				t.Fatal("eviction changed outstanding response bytes")
			}
		})
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
	views, err := app.messageItems(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := app.templates.ExecuteTemplate(&original, "messages", page{Messages: views}); err != nil {
		t.Fatal(err)
	}
	fragment, err := app.messageList(ctx, list)
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
	changed, err := app.messageList(ctx, list)
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
