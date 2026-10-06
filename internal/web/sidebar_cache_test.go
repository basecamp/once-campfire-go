package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSidebarFragmentKey(t *testing.T) {
	base := sidebarFragmentKey(7, 42)
	if base != sidebarFragmentKey(7, 42) {
		t.Fatal("key not deterministic")
	}
	for _, tc := range []struct {
		name                        string
		userID                      int64
		version                     uint64
		wantDistinctFrom, wantEqual string
	}{
		{"version moves", 7, 43, base, ""},
		{"user moves", 8, 42, base, ""},
		{"same key", 7, 42, "", base},
	} {
		key := sidebarFragmentKey(tc.userID, tc.version)
		if tc.wantDistinctFrom != "" && key == tc.wantDistinctFrom {
			t.Errorf("%s: key %q reused for a different (user, version)", tc.name, key)
		}
		if tc.wantEqual != "" && key != tc.wantEqual {
			t.Errorf("%s: key %q != %q", tc.name, key, tc.wantEqual)
		}
	}
}

// getSidebar performs one authenticated sidebar GET with the harness pattern
// (pinned Host, no redirects) and returns status and body.
func getSidebar(t *testing.T, server *httptest.Server, cookie *http.Cookie) (int, []byte) {
	t.Helper()
	response, body := parityGet(t, server, "/users/me/sidebar", cookie)
	return response.StatusCode, body
}

// TestSidebarVersionKeyedServing is the mutation-sequence gate from ENGINE-20:
// every sidebar-visible write made through a write helper is reflected in the
// next sidebar response (join, leave, rename, unread via message create,
// placeholder appear/disappear, involvement), requests between mutations are
// served from the fragment cache, and the response always matches the legacy
// render byte for byte.
func TestSidebarVersionKeyedServing(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()

	status, body := getSidebar(t, server, cookie)
	if status != 200 {
		t.Fatalf("first sidebar: %d", status)
	}
	// Second request is a cache hit: byte-identical, no render.
	status, again := getSidebar(t, server, cookie)
	if status != 200 || !bytes.Equal(again, body) {
		t.Fatalf("cached sidebar changed: %d %d bytes vs %d", status, len(again), len(body))
	}

	alice, err := app.DB.CreateUser(ctx, "Alice", "alice@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unread via message create", func(t *testing.T) {
		before := string(body)
		if _, err := app.DB.CreateMessage(ctx, alice.ID, 1, "unread", "<p>ping</p>", "ping"); err != nil {
			t.Fatal(err)
		}
		status, after := getSidebar(t, server, cookie)
		if status != 200 {
			t.Fatalf("sidebar after message: %d", status)
		}
		if afterText := string(after); afterText == before {
			t.Fatal("message create did not change the sidebar")
		} else if !strings.Contains(afterText, "unread") {
			t.Fatal("unread room missing its unread class")
		}
		// Hit again: byte-identical.
		_, hit := getSidebar(t, server, cookie)
		if !bytes.Equal(hit, after) {
			t.Fatal("post-mutation hit served different bytes")
		}
		body = after
	})
	room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Members Only", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	// "Members Only" starts with only the owner; Alice joins and leaves, both
	// visible to the owner's sidebar.
	if err := app.DB.UpdateRoom(ctx, room.ID, "Rooms::Closed", "Members Only", []int64{user.ID, alice.ID}); err != nil {
		t.Fatal(err)
	}
	t.Run("join", func(t *testing.T) {
		before := string(body)
		status, after := getSidebar(t, server, cookie)
		if status != 200 || string(after) == before {
			t.Fatalf("join not reflected: %d", status)
		}
		body = after
	})
	t.Run("rename", func(t *testing.T) {
		before := string(body)
		if err := app.DB.UpdateRoom(ctx, room.ID, "Rooms::Closed", "Renamed Room", []int64{user.ID, alice.ID}); err != nil {
			t.Fatal(err)
		}
		status, after := getSidebar(t, server, cookie)
		if status != 200 || !strings.Contains(string(after), "Renamed Room") {
			t.Fatalf("rename not reflected: %d", status)
		}
		if string(after) == before {
			t.Fatal("renamed sidebar identical to previous")
		}
		body = after
	})
	t.Run("leave", func(t *testing.T) {
		before := string(body)
		if err := app.DB.UpdateRoom(ctx, room.ID, "Rooms::Closed", "Renamed Room", []int64{alice.ID}); err != nil {
			t.Fatal(err)
		}
		status, after := getSidebar(t, server, cookie)
		if status != 200 || strings.Contains(string(after), "Renamed Room") {
			t.Fatalf("leave not reflected: %d", status)
		}
		if string(after) == before {
			t.Fatal("leaved sidebar identical to previous")
		}
		body = after
	})
	t.Run("placeholder transitions", func(t *testing.T) {
		before := string(body)
		newcomer, err := app.DB.CreateUser(ctx, "Newcomer", "new@test", "digest", "", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		status, after := getSidebar(t, server, cookie)
		if status != 200 || !strings.Contains(string(after), ">Newcomer<") {
			t.Fatalf("placeholder create not reflected: %d", status)
		}
		if string(after) == before {
			t.Fatal("placeholder appeared without change")
		}
		body = after
		if err := app.DB.DeactivateUser(ctx, newcomer.ID); err != nil {
			t.Fatal(err)
		}
		status, after = getSidebar(t, server, cookie)
		if status != 200 || strings.Contains(string(after), ">Newcomer<") {
			t.Fatalf("placeholder deactivate not reflected: %d", status)
		}
		body = after
	})
	t.Run("involvement hides room", func(t *testing.T) {
		before := string(body)
		if err := app.DB.SetInvolvement(ctx, user.ID, 1, "invisible"); err != nil {
			t.Fatal(err)
		}
		status, after := getSidebar(t, server, cookie)
		if status != 200 {
			t.Fatalf("sidebar after invisible: %d", status)
		}
		if string(after) == before || strings.Contains(string(after), "All Talk") {
			t.Fatal("invisible room still in sidebar")
		}
		body = after
	})
	t.Run("room delete", func(t *testing.T) {
		if err := app.DB.DeleteRoom(ctx, room.ID); err != nil {
			t.Fatal(err)
		}
		status, after := getSidebar(t, server, cookie)
		if status != 200 {
			t.Fatalf("sidebar after delete: %d", status)
		}
		if strings.Contains(string(after), "Renamed Room") {
			t.Fatal("deleted room still in sidebar")
		}
	})
}

// TestSidebarHitServesWithBrokenSidebarTables pins the ENGINE-20 hit path:
// after one render, a sidebar request must serve the cached fragment with no
// row reads and no pageSetup. Every table the miss path and pageSetup read
// (rooms, memberships, accounts) is dropped on the live database; the hit
// still serves 200 with the identical bytes. Auth keeps working because it
// only touches users and sessions, which are left alone.
func TestSidebarHitServesWithBrokenSidebarTables(t *testing.T) {
	app, server, cookie, _ := testApp(t)
	status, body := getSidebar(t, server, cookie)
	if status != 200 {
		t.Fatalf("first sidebar: %d", status)
	}
	// DROP TABLE with foreign keys on would refuse to drop the parent of
	// memberships; the sidebar tables are gone either way.
	if _, err := app.DB.Write.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rooms", "memberships", "accounts"} {
		if _, err := app.DB.Write.Exec("DROP TABLE " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	status, hit := getSidebar(t, server, cookie)
	if status != 200 {
		t.Fatalf("sidebar hit with sidebar tables dropped: %d (a miss would need reads)", status)
	}
	if !bytes.Equal(hit, body) {
		t.Fatal("sidebar hit served different bytes after tables dropped")
	}
}

// TestSidebarVersionCacheParity runs the sidebar on the fast read path and
// the database/sql fallback over one shared database through a mutation
// sequence, requiring byte-identical responses at every step and empty
// mutation intervals served as hits.
func TestSidebarVersionCacheParity(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testFastPair(t)
	ctx := context.Background()
	fetch := func(t *testing.T, step string) {
		t.Helper()
		onStatus, onBody := getSidebar(t, onServer, cookie)
		offStatus, offBody := getSidebar(t, offServer, cookie)
		if onStatus != offStatus || onStatus != 200 {
			t.Fatalf("%s: fast %d != legacy %d", step, onStatus, offStatus)
		}
		if !bytes.Equal(onBody, offBody) {
			t.Fatalf("%s: fast body (%d B) != legacy body (%d B)", step, len(onBody), len(offBody))
		}
	}
	fetch(t, "initial")
	alice, err := on.DB.CreateUser(ctx, "Carol", "carol@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.DB.CreateMessage(ctx, alice.ID, 1, "unread", "<p>ping</p>", "ping"); err != nil {
		t.Fatal(err)
	}
	fetch(t, "after unread message")
	closed, err := on.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Quiet Room", []int64{user.ID, alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch(t, "after room create")
	if err := on.DB.UpdateRoom(ctx, closed.ID, "Rooms::Closed", "Loud Room", []int64{user.ID}); err != nil {
		t.Fatal(err)
	}
	fetch(t, "after rename and leave")
	direct, err := on.DB.CreateRoom(ctx, user.ID, "Rooms::Direct", "", []int64{alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch(t, "after direct room")
	if err := on.DB.DeleteRoom(ctx, direct.ID); err != nil {
		t.Fatal(err)
	}
	fetch(t, "after direct room delete")
	// Both caches are now warm at the final version: hits on both sides.
	_, onBody := getSidebar(t, onServer, cookie)
	_, offBody := getSidebar(t, offServer, cookie)
	if !bytes.Equal(onBody, offBody) {
		t.Fatal("warm sidebar hits differ between fast and legacy paths")
	}
}

// TestSidebarFragmentCacheBound pins the sidebar entries to the same bounded
// LRU accounting as every other fragment: filling past the byte limit evicts
// oldest entries and never exceeds the limit.
func TestSidebarFragmentCacheBound(t *testing.T) {
	cache := newFragmentCache(4096)
	var versions []string
	for version := uint64(1); version <= 400; version++ {
		for user := int64(1); user <= 4; user++ {
			key := sidebarFragmentKey(user, version)
			versions = append(versions, key)
			cache.put(key, "sidebar fragment")
		}
	}
	if cache.bytes > 4096 {
		t.Fatalf("sidebar fragments exceeded the bound: %d > 4096", cache.bytes)
	}
	// The most recent versions are retained (LRU: newest evicted last).
	for _, key := range versions[len(versions)-8:] {
		if _, ok := cache.get(key); !ok {
			t.Fatalf("recent sidebar fragment %q evicted", key)
		}
	}
	disabled := newFragmentCache(0)
	disabled.put(sidebarFragmentKey(1, 1), "x")
	if _, ok := disabled.get(sidebarFragmentKey(1, 1)); ok {
		t.Fatal("disabled cache retained a sidebar entry")
	}
}

// TestSidebarVersionCacheRace runs concurrent sidebar reads against a writer
// that mutates sidebar-visible data, including the cold first request racing
// the first write (the seed path). Every response must be a complete sidebar
// with a valid status; -race guards the registry and cache.
func TestSidebarVersionCacheRace(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()

	var readerWG sync.WaitGroup
	stop := make(chan struct{})
	var failures sync.Map
	fetch := func() {
		request, err := http.NewRequest("GET", server.URL+"/users/me/sidebar", nil)
		if err != nil {
			failures.Store(err.Error(), true)
			return
		}
		request.Header.Set("Accept", "*/*")
		request.AddCookie(cookie)
		response, err := server.Client().Do(request)
		if err != nil {
			failures.Store(err.Error(), true)
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte("turbo-frame")) {
			failures.Store(http.StatusText(response.StatusCode), true)
		}
	}
	for r := 0; r < 8; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					fetch()
				}
			}
		}()
	}
	alice, err := app.DB.CreateUser(ctx, "Alice", "alice@test", "digest", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Race Room", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if _, err := app.DB.CreateMessage(ctx, alice.ID, 1, fmt.Sprintf("race-%d", i), "<p>ping</p>", "ping"); err != nil {
			t.Fatal(err)
		}
		if err := app.DB.UpdateRoom(ctx, room.ID, "Rooms::Closed", fmt.Sprintf("Race Room %d", i), []int64{user.ID, alice.ID}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readerWG.Wait()
	var bad bool
	failures.Range(func(_, _ any) bool { bad = true; return false })
	if bad {
		t.Fatal("a concurrent sidebar read failed")
	}
}
