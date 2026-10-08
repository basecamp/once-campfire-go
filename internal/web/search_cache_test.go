package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// testSearchCachePair serves two Server instances over one database: the
// default search result cache (CAMPFIRE_SEARCH_CACHE on) and the
// database/sql fallback (CAMPFIRE_SEARCH_CACHE=off), so the same request can
// be compared byte for byte. Time is frozen so every stamp and piece is
// deterministic.
func testSearchCachePair(t *testing.T) (*Server, *Server, *httptest.Server, *httptest.Server, *http.Cookie, database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("search-cache")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAMPFIRE_SEARCH_CACHE", "on")
	on, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_SEARCH_CACHE", "off")
	off, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	t.Setenv("CAMPFIRE_SEARCH_CACHE", "")
	onServer := httptest.NewServer(front.Deflate(on))
	t.Cleanup(onServer.Close)
	offServer := httptest.NewServer(front.Deflate(off))
	t.Cleanup(offServer.Close)
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	return on, off, onServer, offServer, newSessionCookie(t, secrets, db, user.ID), user
}

// newSessionCookie starts a session for user and returns the signed cookie.
func newSessionCookie(t *testing.T, secrets *rails.Secrets, db *database.DB, user int64) *http.Cookie {
	t.Helper()
	token, err := db.StartSession(context.Background(), user, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
}

// compareSearchExchange requires the on-cache and off-cache responses to be
// byte-identical (decoded), with the same status, ETag and encoding.
func compareSearchExchange(t *testing.T, label string, on, off recordedExchange) {
	t.Helper()
	if on.status != off.status {
		t.Fatalf("%s: status %d != %d", label, on.status, off.status)
	}
	onBody, onErr := decodeRecordedErr(on)
	offBody, offErr := decodeRecordedErr(off)
	if onErr != nil || offErr != nil {
		t.Fatalf("%s: decode on=%v off=%v", label, onErr, offErr)
	}
	if !bytes.Equal(onBody, offBody) {
		t.Fatalf("%s: bodies differ (%d vs %d bytes)", label, len(onBody), len(offBody))
	}
	if on.etag != off.etag {
		t.Errorf("%s: ETag %q != %q", label, on.etag, off.etag)
	}
	if on.encoding != off.encoding {
		t.Errorf("%s: encoding %q != %q", label, on.encoding, off.encoding)
	}
}

func TestSearchResultCacheUnit(t *testing.T) {
	c := newSearchResultCache(1 << 20)
	msg := database.Message{ID: 1, RoomID: 2, CreatorID: 3, ClientID: "c", Body: "body", Creator: "bob"}
	res := searchResult{recent: []string{"alpha"}, messages: []database.Message{msg, msg}}
	if _, ok := c.get(1, "alpha", 0, 0); ok {
		t.Fatal("cold cache hit")
	}
	c.put(1, "alpha", 0, 0, res)
	got, ok := c.get(1, "alpha", 0, 0)
	if !ok || len(got.messages) != 2 || len(got.recent) != 1 || got.recent[0] != "alpha" {
		t.Fatalf("round trip: ok=%v got=%+v", ok, got)
	}
	// Each key component separates entries.
	if _, ok := c.get(1, "alpha", 1, 0); ok {
		t.Fatal("corpus bump served a stale entry")
	}
	if _, ok := c.get(1, "alpha", 0, 1); ok {
		t.Fatal("membership bump served a stale entry")
	}
	if _, ok := c.get(2, "alpha", 0, 0); ok {
		t.Fatal("another user's entry was served")
	}
	if _, ok := c.get(1, "beta", 0, 0); ok {
		t.Fatal("another query's entry was served")
	}
	// A replacement publishes fresh data under the same key.
	c.put(1, "alpha", 0, 0, searchResult{recent: []string{"beta"}})
	got, ok = c.get(1, "alpha", 0, 0)
	if !ok || len(got.recent) != 1 || got.recent[0] != "beta" {
		t.Fatalf("replacement: ok=%v got=%+v", ok, got)
	}
	// purgeUser drops exactly that user's entries.
	c.put(1, "beta", 0, 0, res)
	c.put(2, "alpha", 0, 0, res)
	c.purgeUser(1)
	if _, ok := c.get(1, "alpha", 0, 0); ok {
		t.Fatal("purged entry served")
	}
	if _, ok := c.get(1, "beta", 0, 0); ok {
		t.Fatal("purged entry served")
	}
	if _, ok := c.get(2, "alpha", 0, 0); !ok {
		t.Fatal("purge removed another user's entry")
	}
}

func TestSearchResultCacheBounds(t *testing.T) {
	// An entry larger than limit/4 is rejected outright.
	c := newSearchResultCache(4000)
	big := searchResult{messages: []database.Message{{Body: strings.Repeat("x", 2000)}}}
	c.put(1, "q", 0, 0, big)
	if _, ok := c.get(1, "q", 0, 0); ok {
		t.Fatal("oversized entry was cached")
	}

	// Filling past the budget prunes oldest-first when the byte count crosses
	// the limit (the 75% prune target is only reached on a crossing).
	c2 := newSearchResultCache(60 << 10)
	for i := 0; i < 100; i++ {
		c2.put(1, fmt.Sprintf("q%03d", i), 0, 0, searchResult{messages: []database.Message{{Body: strings.Repeat("m", 600)}}})
	}
	if c2.bytes > c2.limit {
		t.Fatalf("cache holds %d bytes over the %d limit", c2.bytes, c2.limit)
	}
	// 100 entries cannot fit in 60 KiB; eviction must have run and removed
	// the least-recently-used entries first.
	if len(c2.entries) >= 100 {
		t.Fatalf("cache stored all 100 entries (%d bytes)", c2.bytes)
	}
	if _, ok := c2.get(1, "q000", 0, 0); ok {
		t.Fatal("oldest entry survived eviction")
	}
	if _, ok := c2.get(1, "q099", 0, 0); !ok {
		t.Fatal("newest entry was evicted before older ones")
	}

	// A zero budget disables storage but keeps the API usable.
	c3 := newSearchResultCache(0)
	c3.put(1, "q", 0, 0, searchResult{recent: []string{"x"}})
	if _, ok := c3.get(1, "q", 0, 0); ok {
		t.Fatal("zero-budget cache stored an entry")
	}
}

// TestSearchCacheParity pins the on/off byte parity of GET /searches across
// warm reads, gzip, multi-word and empty queries, and a corpus write between
// requests.
func TestSearchCacheParity(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testSearchCachePair(t)
	app := on
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) == 0 {
		t.Fatal("fixture has no rooms")
	}
	for i := 0; i < 4; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("parity-%d", i), "<p>parity alpha beta</p>", "parity alpha beta"); err != nil {
			t.Fatal(err)
		}
	}
	client := recordedClient()
	defer client.CloseIdleConnections()

	for rep := 0; rep < 3; rep++ {
		onGet := recordedGet(t, client, onServer, "/searches?q=parity", "identity", "", cookie)
		offGet := recordedGet(t, client, offServer, "/searches?q=parity", "identity", "", cookie)
		compareSearchExchange(t, fmt.Sprintf("identity rep %d", rep), onGet, offGet)
	}
	compareSearchExchange(t, "gzip", recordedGet(t, client, onServer, "/searches?q=parity", "gzip", "", cookie), recordedGet(t, client, offServer, "/searches?q=parity", "gzip", "", cookie))
	compareSearchExchange(t, "multi-word", recordedGet(t, client, onServer, "/searches?q=parity+beta", "", "", cookie), recordedGet(t, client, offServer, "/searches?q=parity+beta", "", "", cookie))
	compareSearchExchange(t, "empty result", recordedGet(t, client, onServer, "/searches?q=nothing-here", "", "", cookie), recordedGet(t, client, offServer, "/searches?q=nothing-here", "", "", cookie))

	// A corpus write must move both servers onto fresh bytes.
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "parity-gamma", "<p>parity gamma</p>", "parity gamma"); err != nil {
		t.Fatal(err)
	}
	compareSearchExchange(t, "after write", recordedGet(t, client, onServer, "/searches?q=parity", "", "", cookie), recordedGet(t, client, offServer, "/searches?q=parity", "", "", cookie))

	// Conditional GETs stay in sync.
	onGet := recordedGet(t, client, onServer, "/searches?q=parity", "", "", cookie)
	offGet := recordedGet(t, client, offServer, "/searches?q=parity", "", "", cookie)
	if onGet.status != 200 || offGet.status != 200 || onGet.etag == "" || onGet.etag != offGet.etag {
		t.Fatalf("etag setup: on=%d %q off=%d %q", onGet.status, onGet.etag, offGet.status, offGet.etag)
	}
	if got := recordedGet(t, client, onServer, "/searches?q=parity", "", onGet.etag, cookie); got.status != http.StatusNotModified {
		t.Fatalf("on 304: %d", got.status)
	}
	if got := recordedGet(t, client, offServer, "/searches?q=parity", "", offGet.etag, cookie); got.status != http.StatusNotModified {
		t.Fatalf("off 304: %d", got.status)
	}
}

// TestSearchCachePoisoning fills the cache, then mutates the corpus (new
// message, edit, delete) and requires every later response to be fresh — the
// poison test for the corpus version. The final step drops the FTS index and
// requires the cached page to still serve on the cache path while the
// database/sql path fails, proving a hit really skips the FTS scan.
func TestSearchCachePoisoning(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testSearchCachePair(t)
	app := on
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	open := rooms[0]
	closed, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Pouch", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, open.ID, "first", "<p>kangaroo pouch</p>", "kangaroo pouch"); err != nil {
		t.Fatal(err)
	}
	second, err := app.DB.CreateMessage(ctx, user.ID, closed.ID, "second", "<p>kangaroo wallaby</p>", "kangaroo wallaby")
	if err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()

	body := func(x recordedExchange) string { return string(decodeRecorded(t, x)) }

	// Fill.
	onGet := recordedGet(t, client, onServer, "/searches?q=kangaroo", "identity", "", cookie)
	offGet := recordedGet(t, client, offServer, "/searches?q=kangaroo", "identity", "", cookie)
	compareSearchExchange(t, "fill", onGet, offGet)
	if !strings.Contains(body(onGet), "kangaroo pouch") || !strings.Contains(body(onGet), "kangaroo wallaby") {
		t.Fatal("fill did not contain both matches")
	}

	// Edit the closed-room message out of the query's matches: the cached page
	// must not keep serving it.
	newBody, plain := "<p>wallaby only</p>", "wallaby only"
	if _, err := app.DB.UpdateMessageAttributes(ctx, user.ID, second.ID, &newBody, plain, nil); err != nil {
		t.Fatal(err)
	}
	// The frozen clock gives every write one stamp, so the message fragment
	// key would not move and the fragment cache would serve the pre-edit
	// bytes; advance the stamp the way real time would.
	later := "2026-01-02 04:00:00.000000"
	if _, err := app.DB.Write.Exec("UPDATE messages SET created_at=?,updated_at=? WHERE id=?", later, later, second.ID); err != nil {
		t.Fatal(err)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=kangaroo", "identity", "", cookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=kangaroo", "identity", "", cookie)
	compareSearchExchange(t, "after edit", onGet, offGet)
	if strings.Contains(body(onGet), "wallaby") {
		t.Fatal("edited message still in kangaroo results")
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=wallaby", "identity", "", cookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=wallaby", "identity", "", cookie)
	compareSearchExchange(t, "edited term", onGet, offGet)
	if !strings.Contains(body(onGet), "wallaby only") {
		t.Fatal("edited body not searchable")
	}

	// Delete the only wallaby message: no stale result may survive. The page
	// echoes the query itself, so the check targets the message content.
	if err := app.DB.DeleteMessage(ctx, user.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=wallaby", "identity", "", cookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=wallaby", "identity", "", cookie)
	compareSearchExchange(t, "after delete", onGet, offGet)
	if strings.Contains(body(onGet), "wallaby only") {
		t.Fatal("deleted message still in results")
	}

	// Fill once more, then drop the FTS index: the cached path must still
	// serve from pieces while the database/sql path fails.
	onGet = recordedGet(t, client, onServer, "/searches?q=kangaroo", "identity", "", cookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=kangaroo", "identity", "", cookie)
	compareSearchExchange(t, "final fill", onGet, offGet)
	if _, err := app.DB.Write.Exec("DROP TABLE message_search_index"); err != nil {
		t.Fatal(err)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=kangaroo", "identity", "", cookie)
	if onGet.status != 200 || !strings.Contains(body(onGet), "kangaroo pouch") {
		t.Fatalf("cache path did not serve the filled page after DROP: %d", onGet.status)
	}
	offGet = recordedGet(t, client, offServer, "/searches?q=kangaroo", "identity", "", cookie)
	if offGet.status != http.StatusInternalServerError {
		t.Fatalf("database path served after DROP: %d", offGet.status)
	}
}

// TestSearchCacheMembershipInvalidation grants and revokes a membership and
// requires the search results to follow on both paths.
func TestSearchCacheMembershipInvalidation(t *testing.T) {
	on, _, onServer, offServer, _, alice := testSearchCachePair(t)
	app := on
	ctx := context.Background()
	bob, err := app.DB.CreateUser(ctx, "Bob", "bob@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := rails.NewSecrets("search-cache") // cookie signing key must match the pair's
	if err != nil {
		t.Fatal(err)
	}
	bobCookie := newSessionCookie(t, secrets, app.DB, bob.ID)
	burrow, err := app.DB.CreateRoom(ctx, alice.ID, "Rooms::Closed", "Burrow", []int64{alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, alice.ID, burrow.ID, "marmot", "<p>marmot den</p>", "marmot den"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	body := func(x recordedExchange) string { return string(decodeRecorded(t, x)) }

	// Bob has no membership: no results, on either path.
	onGet := recordedGet(t, client, onServer, "/searches?q=marmot", "identity", "", bobCookie)
	offGet := recordedGet(t, client, offServer, "/searches?q=marmot", "identity", "", bobCookie)
	compareSearchExchange(t, "no membership", onGet, offGet)
	if strings.Contains(body(onGet), "marmot den") {
		t.Fatal("closed room leaked to a non-member")
	}

	// Grant: the message must appear.
	if err := app.DB.UpdateRoom(ctx, burrow.ID, "Rooms::Closed", "Burrow", []int64{alice.ID, bob.ID}); err != nil {
		t.Fatal(err)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=marmot", "identity", "", bobCookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=marmot", "identity", "", bobCookie)
	compareSearchExchange(t, "granted", onGet, offGet)
	if !strings.Contains(body(onGet), "marmot den") {
		t.Fatal("granted member did not see the message")
	}

	// Revoke: the message must vanish again.
	if err := app.DB.UpdateRoom(ctx, burrow.ID, "Rooms::Closed", "Burrow", []int64{alice.ID}); err != nil {
		t.Fatal(err)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=marmot", "identity", "", bobCookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=marmot", "identity", "", bobCookie)
	compareSearchExchange(t, "revoked", onGet, offGet)
	if strings.Contains(body(onGet), "marmot den") {
		t.Fatal("revoked member still sees the message")
	}
}

// TestSearchCacheRecentSearchesPurge pins POST/DELETE recent-search semantics
// with the cache on: recording a search purges the user's entries, so the
// next GET shows the fresh recent list on both paths.
func TestSearchCacheRecentSearchesPurge(t *testing.T) {
	_, _, onServer, offServer, cookie, _ := testSearchCachePair(t)
	client := recordedClient()
	defer client.CloseIdleConnections()
	body := func(x recordedExchange) string { return string(decodeRecorded(t, x)) }

	post := func(q string) int {
		return recordedWrite(t, client, onServer, "POST", "/searches", url.Values{"q": {q}}, cookie)
	}
	if status := post("alpha"); status != http.StatusFound {
		t.Fatalf("POST /searches: %d", status)
	}
	onGet := recordedGet(t, client, onServer, "/searches?q=alpha", "identity", "", cookie)
	offGet := recordedGet(t, client, offServer, "/searches?q=alpha", "identity", "", cookie)
	compareSearchExchange(t, "first post", onGet, offGet)
	if !strings.Contains(body(onGet), `href="/searches?q=alpha`) {
		t.Fatal("recorded search missing from recents")
	}

	// A second POST must invalidate the first cached page.
	if status := post("beta"); status != http.StatusFound {
		t.Fatalf("POST /searches: %d", status)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=alpha", "identity", "", cookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=alpha", "identity", "", cookie)
	compareSearchExchange(t, "second post", onGet, offGet)
	if !strings.Contains(body(onGet), `href="/searches?q=beta`) {
		t.Fatal("cached page showed the stale recent list")
	}

	// DELETE /searches/clear empties the recents on both paths.
	if status := recordedWrite(t, client, onServer, "DELETE", "/searches/clear", nil, cookie); status != http.StatusFound {
		t.Fatalf("DELETE /searches/clear: %d", status)
	}
	onGet = recordedGet(t, client, onServer, "/searches?q=alpha", "identity", "", cookie)
	offGet = recordedGet(t, client, offServer, "/searches?q=alpha", "identity", "", cookie)
	compareSearchExchange(t, "cleared", onGet, offGet)
	if strings.Contains(body(onGet), `href="/searches?q=`) {
		t.Fatal("cleared recents still rendered")
	}
}

// TestSearchCacheFuzz runs a deterministic sequence of writes (message
// create/edit/delete, membership grants and revocations, involvement changes)
// interleaved with queries — repeated queries force cache hits — and requires
// byte parity with the cache-off path at every step.
func TestSearchCacheFuzz(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testSearchCachePair(t)
	app := on
	ctx := context.Background()
	partner, err := app.DB.CreateUser(ctx, "Partner", "partner@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	open := rooms[0]
	closed, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Vault", []int64{user.ID, partner.ID})
	if err != nil {
		t.Fatal(err)
	}
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	var messages []database.Message
	for i := 0; i < 6; i++ {
		m, err := app.DB.CreateMessage(ctx, user.ID, open.ID, fmt.Sprintf("fuzz-%d", i), "<p>"+words[i%len(words)]+" noise</p>", words[i%len(words)]+" noise")
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()

	rnd := rand.New(rand.NewSource(1))
	lastQuery := ""
	for step := 0; step < 60; step++ {
		switch op := rnd.Intn(9); op {
		case 0:
			_, err := app.DB.CreateMessage(ctx, user.ID, open.ID, fmt.Sprintf("fuzz-new-%d", step), "<p>"+words[rnd.Intn(len(words))]+" chatter</p>", words[rnd.Intn(len(words))]+" chatter")
			if err != nil {
				t.Fatal(err)
			}
		case 1:
			if len(messages) > 0 {
				m := messages[rnd.Intn(len(messages))]
				body, plain := "<p>"+words[rnd.Intn(len(words))]+" edited</p>", words[rnd.Intn(len(words))]+" edited"
				if _, err := app.DB.UpdateMessageAttributes(ctx, user.ID, m.ID, &body, plain, nil); err != nil {
					t.Fatal(err)
				}
			}
		case 2:
			if len(messages) > 0 {
				i := rnd.Intn(len(messages))
				if err := app.DB.DeleteMessage(ctx, user.ID, messages[i].ID); err == nil {
					messages = append(messages[:i], messages[i+1:]...)
				}
			}
		case 3:
			// Membership grant: open it to the partner, or close it to just
			// the owner.
			if rnd.Intn(2) == 0 {
				if err := app.DB.UpdateRoom(ctx, closed.ID, "Rooms::Closed", "Vault", []int64{user.ID, partner.ID}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := app.DB.UpdateRoom(ctx, closed.ID, "Rooms::Closed", "Vault", []int64{user.ID}); err != nil {
					t.Fatal(err)
				}
			}
		case 4:
			// Involvement changes on the partner in the closed room; the
			// previous op may have revoked their membership, which errors
			// exactly like database/sql's no-rows sentinel and is fine.
			if err := app.DB.SetInvolvement(ctx, partner.ID, closed.ID, []string{"mentions", "everything", "nothing"}[rnd.Intn(3)]); err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
		case 5:
			if _, err := app.DB.CreateMessage(ctx, user.ID, closed.ID, fmt.Sprintf("fuzz-closed-%d", step), "<p>"+words[rnd.Intn(len(words))]+" vault</p>", words[rnd.Intn(len(words))]+" vault"); err != nil {
				t.Fatal(err)
			}
		case 6:
			if _, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", fmt.Sprintf("Vault %d", step), []int64{user.ID, partner.ID}); err != nil {
				t.Fatal(err)
			}
		}
		query := words[rnd.Intn(len(words))]
		if rnd.Intn(4) < 3 && lastQuery != "" {
			query = lastQuery // repeat queries, forcing cache hits
		}
		lastQuery = query
		path := "/searches?q=" + url.QueryEscape(query)
		onGet := recordedGet(t, client, onServer, path, "identity", "", cookie)
		offGet := recordedGet(t, client, offServer, path, "identity", "", cookie)
		compareSearchExchange(t, fmt.Sprintf("step %d q=%s", step, query), onGet, offGet)
	}
}
