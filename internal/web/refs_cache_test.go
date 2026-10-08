package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// ---------------------------------------------------------------------------
// Reference-cache invalidation through the write helpers.

// TestMessageRefsVersionsBumpOnWrites pins the cache's validation contract:
// every message write that changes the room's visible content moves
// rooms.updated_at, which is the pageVersion the reference cache keys on.
// CreateMessage, UpdateMessageWithUpload (a real body change), DeleteMessage,
// CreateBoost and DeleteBoost each must strictly advance the stamp; a no-op
// update (identical body) must not advance it, because nothing changed for
// readers.
func TestMessageRefsVersionsBumpOnWrites(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	roomID := rooms[0].ID
	version := func() time.Time {
		room, err := app.DB.Room(ctx, user.ID, roomID)
		if err != nil {
			t.Fatal(err)
		}
		return room.UpdatedAt
	}
	advance := func(label string, write func() error) {
		t.Helper()
		before := version()
		if err := write(); err != nil {
			t.Fatal(err)
		}
		after := version()
		if !after.After(before) {
			t.Fatalf("%s: rooms.updated_at %v did not advance past %v", label, after, before)
		}
	}
	message, err := app.DB.CreateMessage(ctx, user.ID, roomID, "v1", "<p>version one</p>", "version one")
	if err != nil {
		t.Fatal(err)
	}
	advance("create", func() error {
		_, err := app.DB.CreateMessage(ctx, user.ID, roomID, "v2", "<p>version two</p>", "version two")
		return err
	})
	advance("edit", func() error {
		_, err := app.DB.UpdateMessage(ctx, user.ID, message.ID, "<p>edited</p>", "edited")
		return err
	})
	boost, err := app.DB.CreateBoost(ctx, user.ID, message.ID, "🔥")
	if err != nil {
		t.Fatal(err)
	}
	advance("boost create", func() error {
		_, err := app.DB.CreateBoost(ctx, user.ID, message.ID, "👍")
		return err
	})
	advance("boost delete", func() error {
		return app.DB.DeleteBoost(ctx, user.ID, message.ID, boost.ID)
	})
	advance("delete", func() error {
		return app.DB.DeleteMessage(ctx, user.ID, message.ID)
	})

	// A no-op update must NOT bump the version: the message content is
	// unchanged, so a cached reference window stays valid.
	again, err := app.DB.CreateMessage(ctx, user.ID, roomID, "v3", "<p>noop</p>", "noop")
	if err != nil {
		t.Fatal(err)
	}
	before := version()
	if _, err := app.DB.UpdateMessage(ctx, user.ID, again.ID, "<p>noop</p>", "noop"); err != nil {
		t.Fatal(err)
	}
	if after := version(); !after.Equal(before) {
		t.Fatalf("no-op update advanced rooms.updated_at %v -> %v", before, after)
	}
}

// TestMessageRefsCacheInvalidation drives the invalidation through the server:
// reads warm the cache, a hit serves without rescanning (miss count frozen),
// and every write helper invalidates the window (next read is a miss again)
// and changes the served validator.
func TestMessageRefsCacheInvalidation(t *testing.T) {
	// The refs cache under test sits below the whole-response cache (which
	// would serve warm without consulting it); exercise the refs path itself.
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	roomID := rooms[0].ID
	seed, err := app.DB.CreateMessage(ctx, user.ID, roomID, "seed", "<p>seed</p>", "seed")
	if err != nil {
		t.Fatal(err)
	}
	counters := func() (hits, misses int64) {
		return app.messageRefsHits.Load(), app.messageRefsMisses.Load()
	}
	get := func(t *testing.T, path string) (*http.Response, []byte) {
		t.Helper()
		return perform(t, server, "GET", path, "", nil, cookie)
	}
	// Warm both the messages window and the room window.
	if response, _ := get(t, fmt.Sprintf("/rooms/%d/messages", roomID)); response.StatusCode != 200 {
		t.Fatalf("warm messages: %d", response.StatusCode)
	}
	if response, _ := get(t, fmt.Sprintf("/rooms/%d", roomID)); response.StatusCode != 200 {
		t.Fatalf("warm room: %d", response.StatusCode)
	}
	if _, misses := counters(); misses != 2 {
		t.Fatalf("cold fills: misses %d, want 2 (messages + room)", misses)
	}
	if hits, _ := counters(); hits != 0 {
		t.Fatalf("cold fills: hits %d, want 0", hits)
	}

	// A second round of reads: the room GET and the room anchor GET are
	// distinct windows (anchor 0 vs anchor seed.ID), so the anchor read is a
	// cold fill; the messages and plain room reads hit.
	if response, _ := get(t, fmt.Sprintf("/rooms/%d/messages", roomID)); response.StatusCode != 200 {
		t.Fatalf("hit messages: %d", response.StatusCode)
	}
	if response, _ := get(t, fmt.Sprintf("/rooms/%d/@%d", roomID, seed.ID)); response.StatusCode != 200 {
		t.Fatalf("hit room anchor: %d", response.StatusCode)
	}
	if response, _ := get(t, fmt.Sprintf("/rooms/%d/@%d", roomID, seed.ID)); response.StatusCode != 200 {
		t.Fatalf("hit room anchor again: %d", response.StatusCode)
	}
	if response, _ := get(t, fmt.Sprintf("/rooms/%d", roomID)); response.StatusCode != 200 {
		t.Fatalf("hit room: %d", response.StatusCode)
	}
	if hits, misses := counters(); hits != 3 || misses != 3 {
		t.Fatalf("after warm hits: hits %d misses %d, want 3/3 (messages, room, room anchor)", hits, misses)
	}

	etag := func(t *testing.T, path string) string {
		t.Helper()
		response, _ := get(t, path)
		return response.Header.Get("ETag")
	}
	refill := func(label string, write func() error) {
		t.Helper()
		if err := write(); err != nil {
			t.Fatal(err)
		}
		if _, miss := counters(); miss == 0 {
			t.Fatalf("%s: no cache miss recorded", label)
		}
	}

	refill("create", func() error {
		_, err := app.DB.CreateMessage(ctx, user.ID, roomID, "inv", "<p>invalidation</p>", "invalidation")
		return err
	})
	edited, err := app.DB.CreateMessage(ctx, user.ID, roomID, "edit-target", "<p>before edit</p>", "before edit")
	if err != nil {
		t.Fatal(err)
	}
	before := etag(t, fmt.Sprintf("/rooms/%d/messages", roomID))
	refill("edit", func() error {
		_, err := app.DB.UpdateMessage(ctx, user.ID, edited.ID, "<p>after edit</p>", "after edit")
		return err
	})
	if after := etag(t, fmt.Sprintf("/rooms/%d/messages", roomID)); after == before {
		t.Fatal("edit did not change the messages validator")
	}

	boost, err := app.DB.CreateBoost(ctx, user.ID, edited.ID, "⚡")
	if err != nil {
		t.Fatal(err)
	}
	refill("boost create", func() error {
		_, err := app.DB.CreateBoost(ctx, user.ID, edited.ID, "🚀")
		return err
	})
	refill("boost delete", func() error {
		return app.DB.DeleteBoost(ctx, user.ID, edited.ID, boost.ID)
	})
	refill("delete", func() error {
		return app.DB.DeleteMessage(ctx, user.ID, edited.ID)
	})

	// The room window must refill on the same writes.
	misses := func() int64 { return app.messageRefsMisses.Load() }
	roomRefill := func(label string, write func() error) {
		t.Helper()
		now := misses()
		if err := write(); err != nil {
			t.Fatal(err)
		}
		if response, _ := get(t, fmt.Sprintf("/rooms/%d", roomID)); response.StatusCode != 200 {
			t.Fatalf("%s: room read %d", label, response.StatusCode)
		}
		if misses() <= now {
			t.Fatalf("%s: room window not refilled", label)
		}
	}
	roomRefill("room-invalidate-create", func() error {
		_, err := app.DB.CreateMessage(ctx, user.ID, roomID, "room-inv", "<p>room invalidate</p>", "room invalidate")
		return err
	})
	roomRefill("room-invalidate-edit", func() error {
		_, err := app.DB.UpdateMessage(ctx, user.ID, seed.ID, "<p>seed edited</p>", "seed edited")
		return err
	})
	roomRefill("room-invalidate-delete", func() error {
		return app.DB.DeleteMessage(ctx, user.ID, seed.ID)
	})
}

// ---------------------------------------------------------------------------
// Validator byte parity with the removed per-request messageFreshness.

// TestMessageValidatorParity reconstructs the former messageFreshness ETag
// computation inline and asserts the cached validator keeps that ETag record
// (both Turbo-Frame variants); since upstream b345ea4 the page carries no
// Last-Modified and If-Modified-Since cannot 304 it — the rendered
// representation defines freshness, timestamps alone miss external edits and
// association changes.
func TestMessageValidatorParity(t *testing.T) {
	root := time.Date(2026, 10, 6, 12, 30, 45, 123456000, time.UTC)
	refs := []database.Message{
		{ID: 11, UpdatedAt: root},
		{ID: 22, UpdatedAt: root.Add(2 * time.Second)},
		{ID: 33, UpdatedAt: root.Add(-time.Minute)},
	}
	// The former implementation's ETag, verbatim logic from conditional.go.
	legacy := func(frame bool) string {
		parts := make([]string, 0, len(refs)+2)
		for _, m := range refs {
			parts = append(parts, fmt.Sprintf("messages/%d-%s", m.ID, m.UpdatedAt.UTC().Format("20060102150405.000000")))
			parts[len(parts)-1] = strings.ReplaceAll(parts[len(parts)-1], ".", "")
		}
		if frame {
			parts = append(parts, "frame")
		}
		parts = append(parts, "messages/index")
		hash := sha256.Sum256([]byte(strings.Join(parts, "/")))
		return fmt.Sprintf("W/\"%x\"", hash[:16])
	}
	v := messageValidatorOf(refs)
	if v.etag != legacy(false) {
		t.Fatalf("etag %q vs legacy %q", v.etag, legacy(false))
	}
	// The header dance must reproduce the surviving contract record-for-record.
	// The rendered frame equals the document on this page, so the validator
	// does not vary by the Turbo-Frame header (b345ea4).
	for range 2 {
		r := httptest.NewRequest("GET", "/rooms/1/messages", nil)
		w := httptest.NewRecorder()
		if v.apply(w, r) {
			t.Fatal("unexpected 304 without a conditional header")
		}
		if got := w.Header().Get("ETag"); got != v.etag {
			t.Fatalf("ETag %q", got)
		}
		// If-Modified-Since alone can never 304 the rendered representation.
		r = httptest.NewRequest("GET", "/rooms/1/messages", nil)
		r.Header.Set("If-Modified-Since", root.Add(24*time.Hour).UTC().Format(http.TimeFormat))
		w = httptest.NewRecorder()
		if v.apply(w, r) {
			t.Fatal("a date cannot validate the rendered representation")
		}
		// A matching If-None-Match must 304 and clear the body headers.
		r = httptest.NewRequest("GET", "/rooms/1/messages", nil)
		r.Header.Set("If-None-Match", v.etag)
		w = httptest.NewRecorder()
		if !v.apply(w, r) {
			t.Fatal("matching INM did not 304")
		}
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Fatalf("304 status/body %d", w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Differential byte parity: reference cache on vs off, under every serving
// configuration (recorded pieces and fastdb each on and off).

var loadedAtMask = regexp.MustCompile(`data-refresh-room-loaded-at-value="\d+"`)

// maskLoadedAt strips the per-request refresh timestamp from a room body so
// the two servers' bytes can be compared (the value is millisecond-granular
// wall time by design, even without CAMPFIRE_FROZEN_TIME).
func maskLoadedAt(body []byte) []byte {
	return loadedAtMask.ReplaceAll(body, []byte(`data-refresh-room-loaded-at-value="0"`))
}

type refsExchange struct {
	status                               int
	etag, lastModified, cacheControl, ct string
	body                                 []byte
}

func refsFetch(t *testing.T, client *http.Client, server *httptest.Server, path, ifNoneMatch string, cookie *http.Cookie) refsExchange {
	t.Helper()
	x, err := refsFetchErr(client, server, path, ifNoneMatch, cookie)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// refsFetchErr is the goroutine-safe form of refsFetch.
func refsFetchErr(client *http.Client, server *httptest.Server, path, ifNoneMatch string, cookie *http.Cookie) (refsExchange, error) {
	request, err := http.NewRequest("GET", server.URL+path, nil)
	if err != nil {
		return refsExchange{}, err
	}
	request.Host = "campfire.test"
	request.Header.Set("Accept-Encoding", "identity")
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := client.Do(request)
	if err != nil {
		return refsExchange{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return refsExchange{}, err
	}
	return refsExchange{
		status: response.StatusCode, etag: response.Header.Get("ETag"),
		lastModified: response.Header.Get("Last-Modified"), cacheControl: response.Header.Get("Cache-Control"),
		ct: response.Header.Get("Content-Type"), body: body,
	}, nil
}

// refsPairServers builds two servers over one database, one with the
// reference cache on (8 MiB) and one with it disabled (0), under the serving
// configuration already in the environment. The env toggles are read by New,
// so the caller encodes the configuration before construction.
func refsPairServers(t *testing.T, db *database.DB, secrets *rails.Secrets, root string) (on, off *Server, onServer, offServer *httptest.Server) {
	t.Helper()
	t.Setenv("CAMPFIRE_MESSAGE_REFS_CACHE_MB", "8")
	on, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_MESSAGE_REFS_CACHE_MB", "0")
	off, err = New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	onServer = httptest.NewServer(on)
	t.Cleanup(onServer.Close)
	offServer = httptest.NewServer(off)
	t.Cleanup(offServer.Close)
	return on, off, onServer, offServer
}

// refsPair builds two servers over one fresh database, one with the reference
// cache on (8 MiB) and one with it disabled (0), under the given serving
// configuration.
func refsPair(t *testing.T, recordedPieces, fastdb string) (on, off *Server, onServer, offServer *httptest.Server, cookie *http.Cookie, user database.User, db *database.DB) {
	t.Helper()
	root := t.TempDir()
	set := func(values map[string]string) {
		for key, value := range values {
			t.Setenv(key, value)
		}
	}
	set(map[string]string{
		"CAMPFIRE_RECORDED_PIECES":   recordedPieces,
		"CAMPFIRE_FASTDB":            fastdb,
		"CAMPFIRE_RECORDED_CACHE_MB": "1",
	})
	var err error
	db, err = database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("refs-parity")
	if err != nil {
		t.Fatal(err)
	}
	on, off, onServer, offServer = refsPairServers(t, db, secrets, root)
	user, err = db.Setup(context.Background(), "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(context.Background(), user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return on, off, onServer, offServer, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user, db
}

// compareRefs asserts the cache-on and cache-off exchanges agree on status,
// the messages validator (ETag, Last-Modified, Cache-Control) and the body.
// For the room route only the (loadedAt-masked) body is compared: the legacy
// room ETag hashes the shell bytes around the loadedAt slot, so it is
// per-request by design on both sides.
func compareRefs(t *testing.T, label string, on, off refsExchange) {
	t.Helper()
	if on.status != off.status {
		t.Fatalf("%s: status %d vs %d", label, on.status, off.status)
	}
	if on.status == http.StatusNotModified {
		if on.etag != off.etag {
			t.Fatalf("%s: 304 ETag %q vs %q", label, on.etag, off.etag)
		}
		return
	}
	if strings.Contains(label, "room") && !strings.Contains(label, "messages") {
		if !bytes.Equal(maskLoadedAt(on.body), maskLoadedAt(off.body)) {
			t.Fatalf("%s: room body differs (%d vs %d bytes)", label, len(on.body), len(off.body))
		}
		return
	}
	if on.etag != off.etag {
		t.Fatalf("%s: ETag %q vs %q", label, on.etag, off.etag)
	}
	if on.lastModified != off.lastModified {
		t.Fatalf("%s: Last-Modified %q vs %q", label, on.lastModified, off.lastModified)
	}
	if on.cacheControl != off.cacheControl {
		t.Fatalf("%s: Cache-Control %q vs %q", label, on.cacheControl, off.cacheControl)
	}
	if on.ct != off.ct {
		t.Fatalf("%s: Content-Type %q vs %q", label, on.ct, off.ct)
	}
	if !bytes.Equal(on.body, off.body) {
		t.Fatalf("%s: body differs (%d vs %d bytes)", label, len(on.body), len(off.body))
	}
}

// compareRefsBoth runs one read step on both servers and compares.
func compareRefsBoth(t *testing.T, client *http.Client, on, off *httptest.Server, path, label string, cookie *http.Cookie) (refsExchange, refsExchange) {
	t.Helper()
	x := refsFetch(t, client, on, path, "", cookie)
	y := refsFetch(t, client, off, path, "", cookie)
	compareRefs(t, label, x, y)
	if x.status == 200 && len(x.body) == 0 {
		t.Fatalf("%s: empty 200 body", label)
	}
	return x, y
}

// TestMessageRefsCacheDifferential walks a scripted sequence of writes and
// reads against a cache-on/cache-off server pair for every serving
// configuration, asserting byte-for-byte parity after each step. Writes go
// through the same database helpers the HTTP controllers call, so each step
// exercises invalidation under a real write path. Conditional revalidation is
// cross-checked: the cache-on validator must satisfy the cache-off server and
// vice versa.
func TestMessageRefsCacheDifferential(t *testing.T) {
	client := recordedClient()
	defer client.CloseIdleConnections()
	for _, config := range []struct{ label, pieces, fastdb string }{
		{"pieces+fastdb", "on", "on"},
		{"legacy pieces", "off", "on"},
		{"sql reads", "on", "off"},
		{"fully legacy", "off", "off"},
	} {
		t.Run(config.label, func(t *testing.T) {
			_, _, onServer, offServer, cookie, user, db := refsPair(t, config.pieces, config.fastdb)
			ctx := context.Background()
			rooms, err := db.Rooms(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			room := rooms[0]
			// Seed a populated room; real-clock writes give each message its
			// own stamp so before/after anchors are meaningful.
			var ids []int64
			for i := 0; i < 7; i++ {
				m, err := db.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("seed-%d", i), fmt.Sprintf("<p>seed body %d</p>", i), fmt.Sprintf("seed plain %d", i))
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, m.ID)
			}
			messagesPath := fmt.Sprintf("/rooms/%d/messages", room.ID)
			roomPath := fmt.Sprintf("/rooms/%d", room.ID)
			roomAnchor := func(id int64) string { return fmt.Sprintf("%s/@%d", roomPath, id) }

			// Cold fills on both sides; the validators must agree so the
			// cross-revalidation below is meaningful.
			first, firstOff := compareRefsBoth(t, client, onServer, offServer, messagesPath, "messages last", cookie)
			if first.etag == "" || first.etag != firstOff.etag {
				t.Fatalf("cold ETags differ: %q vs %q", first.etag, firstOff.etag)
			}
			compareRefsBoth(t, client, onServer, offServer, messagesPath, "messages last hit", cookie)
			compareRefsBoth(t, client, onServer, offServer, fmt.Sprintf("%s?before=%d", messagesPath, ids[4]), "messages before", cookie)
			compareRefsBoth(t, client, onServer, offServer, fmt.Sprintf("%s?after=%d", messagesPath, ids[2]), "messages after", cookie)
			compareRefsBoth(t, client, onServer, offServer, roomPath, "room", cookie)
			compareRefsBoth(t, client, onServer, offServer, roomAnchor(ids[3]), "room anchor", cookie)

			// Conditional revalidation: each server revalidates with its own
			// validator and with the other side's.
			for _, otherFirst := range []refsExchange{firstOff, first} {
				x := refsFetch(t, client, onServer, messagesPath, otherFirst.etag, cookie)
				y := refsFetch(t, client, offServer, messagesPath, otherFirst.etag, cookie)
				if x.status != 304 || y.status != 304 {
					t.Fatalf("cross revalidation: %d vs %d", x.status, y.status)
				}
			}
			// A stale validator must revalidate to 200 with a fresh body.
			if stale := refsFetch(t, client, onServer, messagesPath, first.etag, cookie); stale.status != 304 {
				t.Fatalf("fresh window revalidated %d", stale.status)
			}

			// Scripted write steps, each followed by reads that must match
			// the cache-off server byte for byte.
			edited, err := db.CreateMessage(ctx, user.ID, room.ID, "edit-me", "<p>original</p>", "original")
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, edited.ID)
			if _, err := db.UpdateMessage(ctx, user.ID, edited.ID, "<p>edited body</p>", "edited plain"); err != nil {
				t.Fatal(err)
			}
			// The latest window must serve the edited body; the anchored
			// window holds the older messages before the anchor's stamp.
			current, currentOff := compareRefsBoth(t, client, onServer, offServer, messagesPath, "after edit", cookie)
			if !bytes.Contains(current.body, []byte("edited body")) || !bytes.Contains(currentOff.body, []byte("edited body")) {
				t.Fatal("edited body not served")
			}
			compareRefsBoth(t, client, onServer, offServer, fmt.Sprintf("%s?before=%d", messagesPath, edited.ID), "after edit anchored", cookie)

			boost, err := db.CreateBoost(ctx, user.ID, edited.ID, "⚡")
			if err != nil {
				t.Fatal(err)
			}
			compareRefsBoth(t, client, onServer, offServer, messagesPath, "after boost", cookie)
			if err := db.DeleteBoost(ctx, user.ID, edited.ID, boost.ID); err != nil {
				t.Fatal(err)
			}
			compareRefsBoth(t, client, onServer, offServer, messagesPath, "after boost delete", cookie)

			if created, err := db.CreateMessage(ctx, user.ID, room.ID, "fresh", "<p>brand new</p>", "brand new"); err != nil {
				t.Fatal(err)
			} else {
				ids = append(ids, created.ID)
			}
			compareRefsBoth(t, client, onServer, offServer, roomAnchor(edited.ID), "room after create", cookie)

			// Deleting the anchor: the room retry must fall back to the
			// first page on both sides identically.
			if err := db.DeleteMessage(ctx, user.ID, edited.ID); err != nil {
				t.Fatal(err)
			}
			ids = slices.DeleteFunc(ids, func(id int64) bool { return id == edited.ID })
			compareRefsBoth(t, client, onServer, offServer, roomAnchor(edited.ID), "room anchor deleted", cookie)
			compareRefsBoth(t, client, onServer, offServer, roomPath, "room after delete", cookie)
			compareRefsBoth(t, client, onServer, offServer, messagesPath, "messages after delete", cookie)

			// Fuzz phase: a deterministic pseudo-random sequence of writes
			// and reads; every read must stay byte-identical to the
			// cache-off server, and revalidations must agree.
			rng := rand.New(rand.NewSource(19))
			var boostIDs []int64
			lastEtag := make(map[string]string) // path -> validator seen on the cache-on side
			for step := 0; step < 60; step++ {
				switch action := rng.Intn(9); action {
				case 0, 1: // create
					m, err := db.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("fuzz-%d", step), fmt.Sprintf("<p>fuzz %d</p>", rng.Intn(1_000_000)), "fuzz")
					if err != nil {
						t.Fatal(err)
					}
					ids = append(ids, m.ID)
				case 2: // edit a live message
					if len(ids) > 0 {
						id := ids[rng.Intn(len(ids))]
						if _, err := db.UpdateMessage(ctx, user.ID, id, fmt.Sprintf("<p>fuzz edit %d</p>", step), "fuzz edit"); err != nil {
							t.Fatalf("edit %d: %v", step, err)
						}
					}
				case 3: // boost
					if len(ids) > 0 {
						id := ids[rng.Intn(len(ids))]
						b, err := db.CreateBoost(ctx, user.ID, id, "🔥")
						if err != nil {
							t.Fatalf("boost %d: %v", step, err)
						}
						boostIDs = append(boostIDs, b.ID)
					}
				case 4: // delete a boost
					if len(boostIDs) > 0 && len(ids) > 0 {
						pick := rng.Intn(len(boostIDs))
						id := boostIDs[pick]
						boostIDs = append(boostIDs[:pick], boostIDs[pick+1:]...)
						mID := ids[rng.Intn(len(ids))]
						_ = db.DeleteBoost(ctx, user.ID, mID, id)
					}
				case 5: // delete a message (keep at least one)
					if len(ids) > 2 {
						pick := rng.Intn(len(ids))
						id := ids[pick]
						ids = append(ids[:pick], ids[pick+1:]...)
						if err := db.DeleteMessage(ctx, user.ID, id); err != nil {
							t.Fatalf("delete %d: %v", step, err)
						}
					}
				default: // read
					path, label := messagesPath, "fuzz messages"
					switch rng.Intn(4) {
					case 1:
						if len(ids) == 0 {
							continue
						}
						path, label = fmt.Sprintf("%s?before=%d", messagesPath, ids[rng.Intn(len(ids))]), "fuzz before"
					case 2:
						if len(ids) == 0 {
							continue
						}
						path, label = fmt.Sprintf("%s?after=%d", messagesPath, ids[rng.Intn(len(ids))]), "fuzz after"
					case 3:
						path, label = roomPath, "fuzz room"
					}
					etag := lastEtag[path]
					x := refsFetch(t, client, onServer, path, etag, cookie)
					y := refsFetch(t, client, offServer, path, etag, cookie)
					compareRefs(t, fmt.Sprintf("step %d %s", step, label), x, y)
					if x.status == 200 {
						lastEtag[path] = x.etag
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cold race: concurrent readers on one cold cache must agree, and concurrent
// writers must not poison later reads.

func TestMessageRefsCacheColdRace(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.sqlite3")
	db, err := database.Open(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("http-tests")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(db, secrets, false, dbPath, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(app)
	t.Cleanup(server.Close)
	ctx := context.Background()
	user, err := db.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.StartSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := secrets.SignCookie("session_token", token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	rooms, err := db.Rooms(ctx, user.ID)
	roomID := rooms[0].ID
	if _, err := app.DB.CreateMessage(ctx, user.ID, roomID, "cold-1", "<p>cold one</p>", "cold one"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, roomID, "cold-2", "<p>cold two</p>", "cold two"); err != nil {
		t.Fatal(err)
	}
	messagesPath := fmt.Sprintf("/rooms/%d/messages", roomID)
	roomPath := fmt.Sprintf("/rooms/%d", roomID)

	// Phase 1: concurrent cold readers only on one shared cache. Cold fills
	// race with hits; every response must carry the same validator and the
	// same body. All fetches pin the Host so absolute permalinks in the
	// bodies are comparable across servers.
	client := recordedClient()
	defer client.CloseIdleConnections()
	fetch := func(path string) (refsExchange, error) { return refsFetchErr(client, server, path, "", cookie) }
	var wg sync.WaitGroup
	var (
		mu          sync.Mutex
		failure     string
		messageEtag string
		messages    []byte
		room        []byte
	)
	reader := func(path string, strict bool) {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			x, err := fetch(path)
			if err != nil || x.status != 200 {
				mu.Lock()
				failure = fmt.Sprintf("%s: %v status %d", path, err, x.status)
				mu.Unlock()
				return
			}
			if !strict {
				continue // writers are mutating content; the race detector is the assertion
			}
			mu.Lock()
			if strings.HasSuffix(path, "/messages") {
				etag := x.etag
				if messageEtag == "" {
					messageEtag = etag
				} else if etag != messageEtag {
					failure = "messages ETag mismatch"
				}
				masked := maskLoadedAt(x.body)
				if messages == nil {
					messages = masked
				} else if !bytes.Equal(masked, messages) {
					failure = "messages body mismatch"
				}
			} else {
				masked := maskLoadedAt(x.body)
				if room == nil {
					room = masked
				} else if !bytes.Equal(masked, room) {
					failure = "room body mismatch"
				}
			}
			mu.Unlock()
		}
	}
	checkFailure := func(phase string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if failure != "" {
			t.Fatalf("%s: %s", phase, failure)
		}
	}
	readers := 8
	wg.Add(readers)
	for i := 0; i < readers/2; i++ {
		go reader(messagesPath, true)
		go reader(roomPath, true)
	}
	wg.Wait()
	checkFailure("phase 1")

	// Phase 2: writers and readers interleave; reads must stay 200.
	writer := func(n int) {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			m, err := app.DB.CreateMessage(ctx, user.ID, roomID, fmt.Sprintf("race-w%d-%d", n, i), fmt.Sprintf("<p>writer %d %d</p>", n, i), "writer")
			if err != nil {
				mu.Lock()
				failure = err.Error()
				mu.Unlock()
				return
			}
			if i%3 == 0 {
				if _, err := app.DB.UpdateMessage(ctx, user.ID, m.ID, fmt.Sprintf("<p>writer edit %d %d</p>", n, i), "writer edit"); err != nil {
					mu.Lock()
					failure = err.Error()
					mu.Unlock()
					return
				}
			}
			if i%4 == 0 {
				if _, err := app.DB.CreateBoost(ctx, user.ID, m.ID, "💥"); err != nil {
					mu.Lock()
					failure = err.Error()
					mu.Unlock()
					return
				}
			}
		}
	}
	wg.Add(4)
	go writer(1)
	go writer(2)
	for i := 0; i < 2; i++ {
		go reader(messagesPath, false)
	}
	wg.Wait()
	checkFailure("phase 2")

	// Post-chaos correctness: the cache-on server must now serve exactly what
	// a paired cache-off server computes from the same database and the same
	// session (host pinned so permalinks match).
	_, _, offServer, _ := refsPairServers(t, db, secrets, root)
	on, err := refsFetchErr(client, server, messagesPath, "", cookie)
	if err != nil {
		t.Fatal(err)
	}
	off, err := refsFetchErr(client, offServer, messagesPath, "", cookie)
	if err != nil {
		t.Fatal(err)
	}
	if on.status != off.status {
		t.Fatalf("post-chaos status %d vs %d", on.status, off.status)
	}
	if on.status == 200 && !bytes.Equal(on.body, off.body) {
		at := 0
		for at < len(on.body) && at < len(off.body) && on.body[at] == off.body[at] {
			at++
		}
		t.Fatalf("post-chaos messages body differs from cache-off at byte %d of %d/%d: %q vs %q", at, len(on.body), len(off.body), snippet(on.body, at), snippet(off.body, at))
	}
	if on.status == 200 && on.etag != off.etag {
		t.Fatalf("post-chaos validator differs: %q vs %q", on.etag, off.etag)
	}
}

// snippet is a small window around at for failure diagnostics.
func snippet(b []byte, at int) string {
	from := at - 40
	if from < 0 {
		from = 0
	}
	to := at + 40
	if to > len(b) {
		to = len(b)
	}
	return string(b[from:to])
}

// ---------------------------------------------------------------------------
// Benchmarks: what the cache removes on the messages/room read path.

// BenchmarkMessageRefsCache compares the reference window read with the cache
// warm (hit: in-memory lookup + precomputed validator, the ENGINE-19 target)
// against the cache disabled (miss: the 40-row scan plus the per-request
// validator rebuild the profile attributed 19.5%/19.7%+8.1% to), both through
// the fastdb path. The miss server shares the warm server's database and read
// pool; only its reference cache is disabled (CAMPFIRE_MESSAGE_REFS_CACHE_MB=0
// semantics), so the two sides differ solely in the cache.
func BenchmarkMessageRefsCache(b *testing.B) {
	app, _, _, user := testApp(b)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		b.Fatal(err)
	}
	room := rooms[0]
	body := func(i int) string {
		return fmt.Sprintf("<p>Message %d: <strong>%s</strong></p><ul><li>%s</li><li>%s</li></ul>", i, benchSentence(i), benchSentence(i+3), benchSentence(i+11))
	}
	for i := 0; i < 40; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("refs-bench-%d", i), body(i), benchSentence(i)); err != nil {
			b.Fatal(err)
		}
	}
	current, err := app.DB.Room(ctx, user.ID, room.ID)
	if err != nil {
		b.Fatal(err)
	}
	version := current.UpdatedAt
	request := httptest.NewRequest("GET", "/rooms/1", nil)
	c, release := app.fastConn(request)
	defer release()

	// Warm the window the hit bench reads.
	if _, _, err := app.messageRefs(c, ctx, room.ID, 0, "before", version); err != nil {
		b.Fatal(err)
	}
	miss := &Server{DB: app.DB, fastdb: app.fastdb, refsCache: newMessageRefsCache(0)}

	b.Run("hit", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			messages, validator, err := app.messageRefs(c, ctx, room.ID, 0, "before", version)
			if err != nil {
				b.Fatal(err)
			}
			if len(messages) != 40 || validator.etag == "" {
				b.Fatal("hit returned no window")
			}
		}
	})
	b.Run("miss", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			messages, validator, err := miss.messageRefs(c, ctx, room.ID, 0, "before", version)
			if err != nil {
				b.Fatal(err)
			}
			if len(messages) != 40 || validator.etag == "" {
				b.Fatal("miss returned no window")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Bounded cache unit behavior.

func TestMessageRefsCacheBounds(t *testing.T) {
	makeRefs := func(n int) []database.Message {
		refs := make([]database.Message, 0, n)
		for i := 0; i < n; i++ {
			refs = append(refs, database.Message{ID: int64(1000 + i), RoomID: 1, UpdatedAt: time.UnixMicro(1_700_000_000_000_000 + int64(i))})
		}
		return refs
	}
	key := func(room, version, anchor int64, direction string) messageRefsKey {
		return messageRefsKey{room: room, version: version, anchor: anchor, direction: direction}
	}
	value := messageValidatorOf(makeRefs(2))

	// Disabled cache: nothing stores, nothing hits.
	disabled := newMessageRefsCache(0)
	if disabled.Enabled() {
		t.Fatal("zero limit reports enabled")
	}
	disabled.store(key(1, 1, 0, "before"), makeRefs(2), value)
	if _, ok := disabled.lookup(key(1, 1, 0, "before")); ok {
		t.Fatal("zero-limit cache served a stored window")
	}

	// A nil cache (hand-built servers) misses and ignores stores.
	var nilCache *messageRefsCache
	nilCache.store(key(1, 1, 0, "before"), makeRefs(2), value)
	if _, ok := nilCache.lookup(key(1, 1, 0, "before")); ok {
		t.Fatal("nil cache served a window")
	}

	c := newMessageRefsCache(1 << 20)
	// Distinct versions of the same window coexist: the version is part of
	// the key, which is what makes invalidation a key change.
	c.store(key(1, 11, 0, "before"), makeRefs(2), value)
	c.store(key(1, 12, 0, "before"), makeRefs(2), value)
	if e, ok := c.lookup(key(1, 11, 0, "before")); !ok || len(e.refs) != 2 {
		t.Fatal("version 11 window missing")
	}
	if e, ok := c.lookup(key(1, 12, 0, "before")); !ok || len(e.refs) != 2 {
		t.Fatal("version 12 window missing")
	}

	// Stored windows copy their refs: mutating the caller's slice must not
	// reach the cached entry (the cache's immutability contract).
	caller := makeRefs(3)
	c.store(key(2, 21, 0, "around"), caller, value)
	caller[0].ID = 999
	if e, ok := c.lookup(key(2, 21, 0, "around")); !ok || e.refs[0].ID == 999 {
		t.Fatal("cached window aliases the caller's slice")
	}

	// Replacing the same key must not grow the byte accounting.
	before := c.bytes
	for i := 0; i < 5; i++ {
		c.store(key(2, 21, 0, "around"), makeRefs(3), value)
	}
	if c.bytes != before {
		t.Fatalf("replacement changed accounting %d -> %d", before, c.bytes)
	}

	// A window too large for limit/4 is not stored at all.
	before = c.bytes
	c.store(key(3, 31, 0, "before"), makeRefs(1_000_000), value)
	if c.bytes != before {
		t.Fatal("oversized window was stored")
	}

	// Byte bound: a storm of windows stays under the prune threshold (75%).
	huge := newMessageRefsCache(1 << 20)
	for i := 0; i < 2000; i++ {
		huge.store(key(int64(i/10), int64(i), 0, "before"), makeRefs(40), value)
	}
	huge.mu.Lock()
	bytes := huge.bytes
	entries := len(huge.entries)
	huge.mu.Unlock()
	if bytes > huge.limit {
		t.Fatalf("bytes %d exceed limit %d", bytes, huge.limit)
	}
	if bytes > huge.limit*3/4 {
		t.Fatalf("bytes %d above the 75%% prune threshold of %d", bytes, huge.limit*3/4)
	}
	if entries > huge.maxEntries {
		t.Fatalf("entries %d exceed cap %d", entries, huge.maxEntries)
	}

	// Entry cap: the oldest window is evicted first (LRU order).
	small := newMessageRefsCache(1 << 20)
	small.maxEntries = 4
	for i := 0; i < 6; i++ {
		small.store(key(9, int64(i), 0, "before"), makeRefs(2), value)
		if i == 3 {
			// Touch the second-oldest so the cap evicts the oldest, not it.
			small.lookup(key(9, int64(1), 0, "before"))
		}
	}
	if _, ok := small.lookup(key(9, 0, 0, "before")); ok {
		t.Fatal("oldest window survived the entry cap")
	}
	if _, ok := small.lookup(key(9, 5, 0, "before")); !ok {
		t.Fatal("newest window missing after cap eviction")
	}
	small.mu.Lock()
	n := len(small.entries)
	small.mu.Unlock()
	if n > 4 {
		t.Fatalf("%d entries after cap eviction", n)
	}
}
