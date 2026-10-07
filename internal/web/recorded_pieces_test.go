package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/piececache"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/useragent"
)

// testRecordedPair serves two web.Server instances over one database: the
// default piece path and CAMPFIRE_RECORDED_PIECES=off. Both handlers run behind
// front.Deflate, so the differential sees the production encoding composition.
// Time is frozen so the loadedAt piece and every timestamp are deterministic.
func testRecordedPair(t *testing.T) (*Server, *Server, *httptest.Server, *httptest.Server, *http.Cookie, database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "test.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	secrets, err := rails.NewSecrets("recorded-pieces")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "on")
	on, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "off")
	off, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "")
	onServer := httptest.NewServer(front.Deflate(on))
	t.Cleanup(onServer.Close)
	offServer := httptest.NewServer(front.Deflate(off))
	t.Cleanup(offServer.Close)
	user, err := db.Setup(context.Background(), "Owner", "owner@test", "digest")
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
	return on, off, onServer, offServer, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user
}

type recordedExchange struct {
	status        int
	etag          string
	encoding      string
	vary          string
	contentType   string
	contentLength string
	cacheControl  string
	body          []byte
}

func recordedClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func recordedGet(t *testing.T, client *http.Client, server *httptest.Server, path, accept, ifNoneMatch string, cookie *http.Cookie) recordedExchange {
	t.Helper()
	exchange, err := recordedFetch(client, server, path, accept, ifNoneMatch, cookie)
	if err != nil {
		t.Fatal(err)
	}
	return exchange
}

func recordedHead(t *testing.T, client *http.Client, server *httptest.Server, path, accept string, cookie *http.Cookie) recordedExchange {
	t.Helper()
	exchange, err := recordedFetchMethod(client, server, "HEAD", path, accept, "", cookie)
	if err != nil {
		t.Fatal(err)
	}
	return exchange
}

// recordedFetch is the goroutine-safe form of recordedGet.
func recordedFetch(client *http.Client, server *httptest.Server, path, accept, ifNoneMatch string, cookie *http.Cookie) (recordedExchange, error) {
	return recordedFetchMethod(client, server, "GET", path, accept, ifNoneMatch, cookie)
}

func recordedFetchMethod(client *http.Client, server *httptest.Server, method, path, accept, ifNoneMatch string, cookie *http.Cookie) (recordedExchange, error) {
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		return recordedExchange{}, err
	}
	// Both exchanges must render the same origin (absolute URLs, signed stream
	// names); the httptest ports would otherwise differ by construction.
	request.Host = "campfire.test"
	if accept != "" {
		request.Header.Set("Accept-Encoding", accept)
	}
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := client.Do(request)
	if err != nil {
		return recordedExchange{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return recordedExchange{}, err
	}
	return recordedExchange{
		status:        response.StatusCode,
		etag:          response.Header.Get("ETag"),
		encoding:      response.Header.Get("Content-Encoding"),
		vary:          response.Header.Get("Vary"),
		contentType:   response.Header.Get("Content-Type"),
		contentLength: response.Header.Get("Content-Length"),
		cacheControl:  response.Header.Get("Cache-Control"),
		body:          body,
	}, nil
}

func recordedWrite(t *testing.T, client *http.Client, server *httptest.Server, method, path string, form url.Values, cookie *http.Cookie) int {
	t.Helper()
	var body io.Reader
	contentType := ""
	if form != nil {
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	request, err := http.NewRequest(method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "campfire.test"
	request.Header.Set("Accept", "*/*")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.AddCookie(cookie)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func decodeRecorded(t *testing.T, x recordedExchange) []byte {
	t.Helper()
	decoded, err := decodeRecordedErr(x)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// decodeRecordedErr is the goroutine-safe form of decodeRecorded.
func decodeRecordedErr(x recordedExchange) ([]byte, error) {
	switch x.encoding {
	case "":
		return x.body, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(x.body))
		if err != nil {
			return nil, err
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		if err := reader.Close(); err != nil {
			return nil, err
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unexpected Content-Encoding %q", x.encoding)
	}
}

// compareRecorded asserts the piece path and the legacy path agree on status,
// negotiated encoding and decoded body. Encoded bytes differ by construction:
// the piece path assembles ONE gzip member spliced from deflate fragments
// (single-member, browser-safe) where front.Deflate emits its own single
// member; ETag values differ for the room route by the documented
// loadedAt-free scheme.
func compareRecorded(t *testing.T, label string, pieces, legacy recordedExchange) {
	t.Helper()
	if pieces.status != legacy.status {
		t.Fatalf("%s: status %d vs %d", label, pieces.status, legacy.status)
	}
	if pieces.encoding != legacy.encoding {
		t.Fatalf("%s: Content-Encoding %q vs %q", label, pieces.encoding, legacy.encoding)
	}
	if !strings.Contains(strings.ToLower(pieces.vary), "accept-encoding") {
		t.Fatalf("%s: Vary %q lacks Accept-Encoding", label, pieces.vary)
	}
	if pieces.contentType != legacy.contentType {
		t.Fatalf("%s: Content-Type %q vs %q", label, pieces.contentType, legacy.contentType)
	}
	if pieces.cacheControl != legacy.cacheControl {
		t.Fatalf("%s: Cache-Control %q vs %q", label, pieces.cacheControl, legacy.cacheControl)
	}
	// A declared Content-Length must describe the bytes actually received on
	// that side; the spliced gzip body differs in length from the legacy
	// single member, so cross-side comparison is only meaningful for identity.
	for name, x := range map[string]recordedExchange{"pieces": pieces, "legacy": legacy} {
		if x.contentLength != "" && x.contentLength != strconv.Itoa(len(x.body)) {
			t.Fatalf("%s: %s Content-Length %s, body %d bytes", label, name, x.contentLength, len(x.body))
		}
	}
	if pieces.encoding == "" && pieces.contentLength != legacy.contentLength {
		t.Fatalf("%s: identity Content-Length %q vs %q", label, pieces.contentLength, legacy.contentLength)
	}
	pieceBody := decodeRecorded(t, pieces)
	legacyBody := decodeRecorded(t, legacy)
	if !bytes.Equal(pieceBody, legacyBody) {
		t.Fatalf("%s: decoded body differs (%d vs %d bytes)", label, len(pieceBody), len(legacyBody))
	}
}

func TestRecordedPiecesBodyEquality(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testRecordedPair(t)
	ctx := context.Background()
	rooms, err := on.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	var firstID int64
	for i := 0; i < 3; i++ {
		message, err := on.DB.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("seed-%d", i), fmt.Sprintf("<p>recorded seed %d</p>", i), fmt.Sprintf("recorded seed %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if firstID == 0 {
			firstID = message.ID
		}
	}
	direct, err := on.DB.CreateRoom(ctx, user.ID, "Rooms::Direct", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := on.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Closed & quiet", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	cases := []struct{ label, path, accept string }{
		{"room gzip", fmt.Sprintf("/rooms/%d", room.ID), "gzip"},
		{"room identity", fmt.Sprintf("/rooms/%d", room.ID), "identity"},
		{"room default", fmt.Sprintf("/rooms/%d", room.ID), ""},
		{"room gzip refused", fmt.Sprintf("/rooms/%d", room.ID), "gzip;q=0"},
		{"room wildcard", fmt.Sprintf("/rooms/%d", room.ID), "*"},
		{"room gzip and identity", fmt.Sprintf("/rooms/%d", room.ID), "gzip, identity"},
		{"room gzip half", fmt.Sprintf("/rooms/%d", room.ID), "gzip;q=0.5"},
		{"room anchor gzip", fmt.Sprintf("/rooms/%d/@%d", room.ID, firstID), "gzip"},
		{"room identity wins", fmt.Sprintf("/rooms/%d", room.ID), "gzip;q=0.5, identity;q=1"},
		{"messages gzip", fmt.Sprintf("/rooms/%d/messages", room.ID), "gzip"},
		{"messages identity", fmt.Sprintf("/rooms/%d/messages", room.ID), "identity"},
		{"search gzip", "/searches?q=recorded", "gzip"},
		{"search identity", "/searches?q=recorded", "identity"},
		{"direct room gzip", fmt.Sprintf("/rooms/%d", direct.ID), "gzip"},
		{"closed room identity", fmt.Sprintf("/rooms/%d", closed.ID), "identity"},
	}
	for pass := 0; pass < 2; pass++ {
		for _, c := range cases {
			label := fmt.Sprintf("pass %d %s", pass, c.label)
			pieces := recordedGet(t, client, onServer, c.path, c.accept, "", cookie)
			legacy := recordedGet(t, client, offServer, c.path, c.accept, "", cookie)
			compareRecorded(t, label, pieces, legacy)
			if pieces.status == 200 && len(pieces.body) == 0 {
				t.Fatalf("%s: empty body", label)
			}
			// The room route's ETag deliberately excludes the per-request
			// loadedAt value; every other recorded route reproduces legacy's
			// validator record for record.
			if !strings.Contains(c.label, "room") && pieces.etag != legacy.etag {
				t.Fatalf("%s: ETag %q vs %q", label, pieces.etag, legacy.etag)
			}
		}
	}
}

func TestRecordedPiecesConditionalSkipsAssembly(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "conditional", "<p>conditional</p>", "conditional"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	first := recordedGet(t, client, server, path, "gzip", "", cookie)
	if first.status != 200 || first.etag == "" {
		t.Fatalf("first response: %d etag=%q", first.status, first.etag)
	}
	assemblies := app.recordedAssemblies.Load()
	second := recordedGet(t, client, server, path, "gzip", first.etag, cookie)
	if second.status != http.StatusNotModified || len(second.body) != 0 {
		t.Fatalf("conditional response: %d with %d bytes", second.status, len(second.body))
	}
	if got := app.recordedAssemblies.Load(); got != assemblies {
		t.Fatalf("304 path assembled %d time(s)", got-assemblies)
	}
	// A changed content key misses the cached pieces and returns a fresh body.
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "changed", "<p>changed key</p>", "changed key"); err != nil {
		t.Fatal(err)
	}
	third := recordedGet(t, client, server, path, "gzip", first.etag, cookie)
	if third.status != 200 || !bytes.Contains(decodeRecorded(t, third), []byte("changed key")) {
		t.Fatalf("changed key response: %d", third.status)
	}
	if app.recordedAssemblies.Load() == assemblies {
		t.Fatal("changed key did not assemble a new body")
	}
}

func TestRecordedShellKeySeparatesMessageCount(t *testing.T) {
	base := page{Room: database.Room{ID: 7, Name: "Room & <name>", Type: "Rooms::Open"}, Screen: "search", Query: "q"}
	none := recordedShellIdentity("search", base)
	one := base
	one.Messages = make([]messageView, 1)
	oneKey := recordedShellIdentity("search", one)
	two := base
	two.Messages = make([]messageView, 2)
	twoKey := recordedShellIdentity("search", two)
	if none == oneKey || none == twoKey || oneKey == twoKey {
		t.Fatalf("message count does not separate shell identities: %x %x %x", none, oneKey, twoKey)
	}
	// The route name separates shells too.
	if other := recordedShellIdentity("room", base); other == none {
		t.Fatalf("route name does not separate shell identities: %x", other)
	}
}

// TestRecordedPiecesColdRace serves a fresh, uncached page from many goroutines
// at once and requires every decoded body to equal the legacy oracle. Two
// concurrent first renders must store identical pieces (the split points are
// marker-independent), so no request can observe a half-stored shell or a
// payload mixed from another render.
func TestRecordedPiecesColdRace(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testRecordedPair(t)
	ctx := context.Background()
	rooms, err := on.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := on.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("race-%d", i), fmt.Sprintf("<p>race message %d</p>", i), fmt.Sprintf("race message %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	client := recordedClient()
	defer client.CloseIdleConnections()
	oracle := recordedGet(t, client, offServer, path, "gzip", "", cookie)
	if oracle.status != 200 {
		t.Fatalf("oracle status %d", oracle.status)
	}
	want := decodeRecorded(t, oracle)
	// No warm-up: the piece-path server has never rendered this page.
	const workers = 16
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			response, err := recordedFetch(client, onServer, path, "gzip", "", cookie)
			if err != nil {
				results <- err
				return
			}
			decoded, err := decodeRecordedErr(response)
			if err != nil {
				results <- err
				return
			}
			if response.status != 200 {
				results <- fmt.Errorf("status %d", response.status)
				return
			}
			if !bytes.Equal(decoded, want) {
				results <- fmt.Errorf("cold-race body differs (%d vs %d bytes)", len(decoded), len(want))
				return
			}
			results <- nil
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// TestRecordedPiecesPayloadReplacement races concurrent first renders of the
// same message list. Every caller must receive the same, complete payload
// bytes even though they contend to publish the entry under one key.
func TestRecordedPiecesPayloadReplacement(t *testing.T) {
	app, _, _, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("payload-race-%d", i), fmt.Sprintf("<p>payload race %d</p>", i), fmt.Sprintf("payload race %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := app.DB.MessagePageReferences(ctx, rooms[0].ID, 0, "around")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 12
	type result struct {
		raw []byte
		err error
	}
	results := make(chan result, workers)
	for i := 0; i < workers; i++ {
		go func() {
			payload, err := app.recordedMessageList(ctx, messages, true, false)
			if err != nil {
				results <- result{err: err}
				return
			}
			if payload.piece == nil {
				results <- result{err: fmt.Errorf("no piece")}
				return
			}
			results <- result{raw: payload.piece.Raw}
		}()
	}
	var first []byte
	for i := 0; i < workers; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !bytes.Contains(got.raw, []byte("payload race 7")) {
			t.Fatalf("payload %d bytes lacks the last message", len(got.raw))
		}
		if first == nil {
			first = got.raw
			continue
		}
		if !bytes.Equal(first, got.raw) {
			t.Fatalf("concurrent payloads differ (%d vs %d bytes)", len(first), len(got.raw))
		}
	}
	// The published entry is the same immutable payload.
	identity := messageListIdentity(messages)
	stored := app.pieces.GetDigest(identity)
	if stored == nil || !bytes.Equal(stored.Raw, first) {
		t.Fatal("cache entry does not match the returned payload")
	}
}

func TestRecordedPiecesInvalidateOnWrites(t *testing.T) {
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	roomPath := fmt.Sprintf("/rooms/%d", room.ID)
	messagesPath := fmt.Sprintf("/rooms/%d/messages", room.ID)
	searchPath := "/searches?q=invalidation"
	client := recordedClient()
	defer client.CloseIdleConnections()
	// Warm every recorded cache with the pre-write state.
	recordedGet(t, client, server, roomPath, "gzip", "", cookie)
	recordedGet(t, client, server, messagesPath, "gzip", "", cookie)
	recordedGet(t, client, server, searchPath, "gzip", "", cookie)

	if status := recordedWrite(t, client, server, "POST", messagesPath, url.Values{"message[body]": {"<p>invalidation marker</p>"}}, cookie); status >= 400 {
		t.Fatalf("post status %d", status)
	}
	roomResponse := recordedGet(t, client, server, roomPath, "gzip", "", cookie)
	if roomResponse.status != 200 || !bytes.Contains(decodeRecorded(t, roomResponse), []byte("invalidation marker")) {
		t.Fatalf("room did not reflect the post: %d", roomResponse.status)
	}
	messages := recordedGet(t, client, server, messagesPath, "gzip", "", cookie)
	if messages.status != 200 || !bytes.Contains(decodeRecorded(t, messages), []byte("invalidation marker")) {
		t.Fatalf("messages did not reflect the post: %d", messages.status)
	}
	search := recordedGet(t, client, server, searchPath, "gzip", "", cookie)
	if search.status != 200 || !bytes.Contains(decodeRecorded(t, search), []byte("invalidation marker")) {
		t.Fatalf("search did not reflect the post: %d", search.status)
	}
	var messageID int64
	if err := app.DB.Read.QueryRowContext(ctx, "SELECT id FROM messages WHERE room_id=? ORDER BY id DESC LIMIT 1", room.ID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}

	// Edit through the normal write path (POST with _method=PATCH).
	if status := recordedWrite(t, client, server, "POST", fmt.Sprintf("/rooms/%d/messages/%d", room.ID, messageID), url.Values{"_method": {"PATCH"}, "message[body]": {"<p>edited marker</p>"}}, cookie); status >= 400 {
		t.Fatalf("edit status %d", status)
	}
	roomResponse = recordedGet(t, client, server, roomPath, "gzip", "", cookie)
	edited := decodeRecorded(t, roomResponse)
	if !bytes.Contains(edited, []byte("edited marker")) || bytes.Contains(edited, []byte("invalidation marker")) {
		t.Fatal("room did not reflect the edit")
	}
	// The edit also changes search: q=invalidation no longer matches the
	// message, so a stale cached result must not survive.
	search = recordedGet(t, client, server, searchPath, "gzip", "", cookie)
	if search.status != 200 || bytes.Contains(decodeRecorded(t, search), []byte("invalidation marker")) {
		t.Fatalf("search still serves the pre-edit content: %d", search.status)
	}

	// A room rename feeds the shell key.
	if err := app.DB.UpdateRoom(ctx, room.ID, "Rooms::Open", "Renamed & Room", nil); err != nil {
		t.Fatal(err)
	}
	roomResponse = recordedGet(t, client, server, roomPath, "gzip", "", cookie)
	if !bytes.Contains(decodeRecorded(t, roomResponse), []byte("Renamed &amp; Room")) {
		t.Fatal("room did not reflect the rename")
	}

	// Delete through the normal write path.
	if status := recordedWrite(t, client, server, "POST", fmt.Sprintf("/rooms/%d/messages/%d", room.ID, messageID), url.Values{"_method": {"DELETE"}}, cookie); status >= 400 {
		t.Fatalf("delete status %d", status)
	}
	roomResponse = recordedGet(t, client, server, roomPath, "gzip", "", cookie)
	deleted := decodeRecorded(t, roomResponse)
	if bytes.Contains(deleted, []byte("edited marker")) || bytes.Contains(deleted, []byte("invalidation marker")) {
		t.Fatal("room still serves the deleted message")
	}
	search = recordedGet(t, client, server, searchPath, "gzip", "", cookie)
	stale := decodeRecorded(t, search)
	if bytes.Contains(stale, []byte("edited marker")) || bytes.Contains(stale, []byte("invalidation marker")) {
		t.Fatal("search still serves the deleted message")
	}
	// A search for the edited text must also drop the message after deletion.
	search = recordedGet(t, client, server, "/searches?q=edited", "gzip", "", cookie)
	if bytes.Contains(decodeRecorded(t, search), []byte("edited marker")) {
		t.Fatal("search for the edited text still serves the deleted message")
	}

	// Membership changes feed the search page's message list (Search joins
	// memberships); losing access must drop the cached result.
	memberRoom, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Members only", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, memberRoom.ID, "member", "<p>membership marker</p>", "membership marker"); err != nil {
		t.Fatal(err)
	}
	search = recordedGet(t, client, server, "/searches?q=membership", "gzip", "", cookie)
	if !bytes.Contains(decodeRecorded(t, search), []byte("membership marker")) {
		t.Fatal("search did not see the scoped message")
	}
	if err := app.DB.UpdateRoom(ctx, memberRoom.ID, "Rooms::Closed", "Members only", nil); err != nil {
		t.Fatal(err)
	}
	search = recordedGet(t, client, server, "/searches?q=membership", "gzip", "", cookie)
	if bytes.Contains(decodeRecorded(t, search), []byte("membership marker")) {
		t.Fatal("search still serves a room the user lost access to")
	}
}

func TestRecordedPiecesConcurrent(t *testing.T) {
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("concurrent-%d", i), fmt.Sprintf("<p>concurrent %d</p>", i), fmt.Sprintf("concurrent %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	paths := []string{
		fmt.Sprintf("/rooms/%d", rooms[0].ID),
		fmt.Sprintf("/rooms/%d/messages", rooms[0].ID),
		"/searches?q=concurrent",
	}
	// Hammer each path concurrently; a first-miss race must still produce the
	// same bytes as the warm hit, never a half-stored piece mix.
	baseline := make([]string, len(paths))
	for i, path := range paths {
		baseline[i] = string(decodeRecorded(t, recordedGet(t, client, server, path, "gzip", "", cookie)))
	}
	results := make(chan error, 24)
	for i := 0; i < 24; i++ {
		path := paths[i%len(paths)]
		index := i % len(paths)
		go func() {
			response, err := recordedFetch(client, server, path, "gzip", "", cookie)
			if err != nil {
				results <- err
				return
			}
			if response.status != 200 {
				results <- fmt.Errorf("%s: status %d", path, response.status)
				return
			}
			decoded, err := decodeRecordedErr(response)
			if err != nil {
				results <- err
				return
			}
			if string(decoded) != baseline[index] {
				results <- fmt.Errorf("%s: body mismatch", path)
				return
			}
			results <- nil
		}()
	}
	for i := 0; i < 24; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// TestRecordedPiecesLoadedAtAdvance pins the room route's ETag contract when
// the clock moves but every content key stays fixed: the body changes (the
// loadedAt timestamp is a per-request piece) while the validator stays stable,
// so a conditional request still receives 304.
func TestRecordedPiecesLoadedAtAdvance(t *testing.T) {
	on, _, onServer, _, cookie, user := testRecordedPair(t)
	ctx := context.Background()
	rooms, err := on.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "loaded-at", "<p>loaded at</p>", "loaded at"); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli())
	on.DB.Now = func() time.Time { return time.UnixMilli(clock.Load()) }
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	first := recordedGet(t, client, onServer, path, "gzip", "", cookie)
	if first.status != 200 || first.etag == "" {
		t.Fatalf("first response: %d etag=%q", first.status, first.etag)
	}
	clock.Add(1000)
	second := recordedGet(t, client, onServer, path, "gzip", "", cookie)
	if second.status != 200 {
		t.Fatalf("second response: %d", second.status)
	}
	if bytes.Equal(decodeRecorded(t, first), decodeRecorded(t, second)) {
		t.Fatal("advancing loadedAt did not change the body")
	}
	if second.etag != first.etag {
		t.Fatalf("room ETag moved with loadedAt: %q vs %q", second.etag, first.etag)
	}
	conditional := recordedGet(t, client, onServer, path, "gzip", first.etag, cookie)
	if conditional.status != http.StatusNotModified || len(conditional.body) != 0 {
		t.Fatalf("conditional after loadedAt advance: %d with %d bytes", conditional.status, len(conditional.body))
	}
}

// TestRecordedPiecesHeadSkipsAssembly pins the HEAD contract: no body, the
// Content-Length the GET response would carry, and no assembly work.
func TestRecordedPiecesHeadSkipsAssembly(t *testing.T) {
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, fmt.Sprintf("head-%d", i), fmt.Sprintf("<p>head %d</p>", i), fmt.Sprintf("head %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	gzipGet := recordedGet(t, client, server, path, "gzip", "", cookie)
	if gzipGet.encoding != "gzip" || gzipGet.contentLength == "" {
		t.Fatalf("gzip GET: encoding=%q length=%q", gzipGet.encoding, gzipGet.contentLength)
	}
	assemblies := app.recordedAssemblies.Load()
	gzipHead := recordedHead(t, client, server, path, "gzip", cookie)
	if gzipHead.status != 200 || len(gzipHead.body) != 0 {
		t.Fatalf("gzip HEAD: %d with %d bytes", gzipHead.status, len(gzipHead.body))
	}
	if gzipHead.encoding != "gzip" || gzipHead.contentLength != gzipGet.contentLength {
		t.Fatalf("gzip HEAD: encoding=%q length=%q, GET length=%q", gzipHead.encoding, gzipHead.contentLength, gzipGet.contentLength)
	}
	if got := app.recordedAssemblies.Load(); got != assemblies {
		t.Fatalf("HEAD assembled %d time(s)", got-assemblies)
	}
	identityGet := recordedGet(t, client, server, path, "identity", "", cookie)
	identityHead := recordedHead(t, client, server, path, "identity", cookie)
	if identityHead.status != 200 || len(identityHead.body) != 0 {
		t.Fatalf("identity HEAD: %d with %d bytes", identityHead.status, len(identityHead.body))
	}
	if identityHead.contentLength != identityGet.contentLength || identityHead.encoding != "" {
		t.Fatalf("identity HEAD: length=%q encoding=%q, GET length=%q", identityHead.contentLength, identityHead.encoding, identityGet.contentLength)
	}
}

// TestClientAcceptsGzipParityWithFront drives front.Deflate with an identity
// handler so the middleware's own negotiation decides, then asserts that
// whenever the web layer would pre-encode, front selected gzip for the same
// header. A duplicate-token header is the case where a naive parser and the
// middleware's ranking can disagree.
func TestClientAcceptsGzipParityWithFront(t *testing.T) {
	server := httptest.NewServer(front.Deflate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "probe")
	})))
	defer server.Close()
	client := recordedClient()
	defer client.CloseIdleConnections()
	headers := []string{
		"", "gzip", "gzip;q=0.5", "gzip, identity", "gzip;q=0", "*", "identity", "br",
		"gzip;q=1, identity;q=1", "gzip;q=2", "gzip;q=0.999",
		"gzip;q=0, gzip;q=1", "gzip;q=1, gzip;q=0", "GZIP", "gzip ; q=1",
	}
	for _, header := range headers {
		request, err := http.NewRequest("GET", server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if header != "" {
			request.Header.Set("Accept-Encoding", header)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		selectedGzip := response.Header.Get("Content-Encoding") == "gzip"
		if clientAcceptsGzip(request) && !selectedGzip {
			t.Fatalf("Accept-Encoding %q: web would pre-encode but front chose identity/406 (status %d)", header, response.StatusCode)
		}
	}
}

func TestRecordedPiecesSingleGzipLayer(t *testing.T) {
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "layer", "<p>one layer</p>", "one layer"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	identity := recordedGet(t, client, server, path, "identity", "", cookie)
	gz := recordedGet(t, client, server, path, "gzip", "", cookie)
	if gz.encoding != "gzip" {
		t.Fatalf("gzip response Content-Encoding = %q", gz.encoding)
	}
	if !bytes.Equal(decodeRecorded(t, gz), identity.body) {
		t.Fatal("gzip body did not decode to the identity body")
	}
	if !strings.Contains(strings.ToLower(identity.vary), "accept-encoding") {
		t.Fatalf("identity Vary = %q", identity.vary)
	}
	if !strings.Contains(strings.ToLower(gz.vary), "accept-encoding") {
		t.Fatalf("gzip Vary = %q", gz.vary)
	}
	// The identity request must not carry an encoding header, and the same
	// cache key must not produce different bytes for the two encodings.
	if identity.encoding != "" {
		t.Fatalf("identity response encoded as %q", identity.encoding)
	}
}

// TestRecordedCompressorPoolPoisoning proves a pooled compressor's buffer
// cannot leak one piece's bytes into the next: the buffer is reset before use,
// the writer is reset to nil on return, and every caller gets an independent
// copy. Poisoning the pooled buffer directly is the strongest form.
func TestRecordedShellSplitGuards(t *testing.T) {
	if _, err := splitRecordedShell([]byte("no markers here"), "loaded", "message"); err == nil {
		t.Fatal("missing message marker accepted")
	}
	if _, err := splitRecordedShell([]byte("before\x00m\x00middle\x00m\x00after"), "", "\x00m\x00"); err == nil {
		t.Fatal("repeated message marker accepted")
	}
	if _, err := splitRecordedShell([]byte("aLOADb\x00m\x00cLOADd"), "LOAD", "\x00m\x00"); err == nil {
		t.Fatal("repeated loaded marker accepted")
	}
	layout, err := splitRecordedShell([]byte("A$L$B\x00m\x00C"), "L", "\x00m\x00")
	if err != nil {
		t.Fatal(err)
	}
	if layout.count != 3 || layout.slots[0] != slotLoadedAt || layout.slots[1] != slotMessages {
		t.Fatalf("layout %+v", layout)
	}
	if string(layout.segments[0]) != "A$" || string(layout.segments[1]) != "$B" || string(layout.segments[2]) != "C" {
		t.Fatalf("segments %q %q %q", layout.segments[0], layout.segments[1], layout.segments[2])
	}
}

// TestRecordedCompressorPoolPoisoning proves a pooled compressor's buffer
// cannot leak one piece's bytes into the next: the buffer is reset before use,
// the writer is reset to nil on return, and every caller gets an independent
// copy. Poisoning the pooled buffer directly is the strongest form. The
// fragments are not self-decoding, so each is spliced into its own single
// gzip member for the read-back.
func TestRecordedCompressorPoolPoisoning(t *testing.T) {
	poison := compressorPool(9).Get().(*recordedCompressor)
	poison.buf.WriteString(strings.Repeat("POISON", 64))
	poison.writer.Reset(nil)
	compressorPool(9).Put(poison)

	first := compressFragment([]byte("first payload"))
	second := compressFragment([]byte("second payload"))
	for name, tc := range map[string]struct {
		fragment []byte
		want     string
	}{"first": {first, "first payload"}, "second": {second, "second payload"}} {
		entry := piececache.NewEntry([]byte(tc.want), tc.fragment, nil)
		assembled, _, err := piececache.Assemble(nil, piececache.Gzip, entry)
		if err != nil {
			t.Fatalf("%s: assemble: %v", name, err)
		}
		reader, err := gzip.NewReader(bytes.NewReader(assembled))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(decoded) != tc.want {
			t.Fatalf("%s: decoded %q, want %q", name, decoded, tc.want)
		}
	}
}

func TestClientAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br, zstd", true},
		{"gzip;q=1", true},
		{"gzip;q=0", false},
		{"gzip;q=0.5", true},
		{"gzip, identity", true},
		{"gzip, identity;q=0", true},
		{"identity", false},
		{"*", true},
		{"br", false},
		{"gzip;q=2", true},
		{"gzip;q=0.999", true},
		{"gzip;q=0.5, identity;q=1", false},
		{"identity;q=0, identity", false},
		// Duplicate tokens: front's selector rejects a name on any q=0
		// occurrence, so these must agree with it exactly.
		{"gzip;q=0, gzip;q=1", false},
		{"gzip, gzip;q=0", false},
		{"gzip;q=0, gzip", false},
		{"gzip ; q=1", true},
		// The selector is case-sensitive, as the middleware always was.
		{"GZIP", false},
	}
	for _, c := range cases {
		request := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			request.Header.Set("Accept-Encoding", c.header)
		}
		if got := clientAcceptsGzip(request); got != c.want {
			t.Errorf("Accept-Encoding %q: got %v, want %v", c.header, got, c.want)
		}
	}
}

// TestRecordedShellIdentityAudit mutates every page input the recorded
// templates read and asserts each one changes the shell identity. The list
// mirrors appendShellIdentity: if a template starts reading a new page field
// without that field being added there, two different pages could share a
// shell, so this test is the audit for that list.
func TestRecordedShellIdentityAudit(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	later := stamp.Add(time.Second)
	base := page{
		Title: "Room", Frame: false, Reload: false, Chat: true, Screen: "room",
		BodyClass: "sidebar", Notice: "notice", Error: "error", BackPath: "/",
		Version: "v1", VAPIDPublicKey: "key", CustomStyles: template.HTML("<style>x</style>"),
		User:    database.User{ID: 3, Name: "Ada", Bio: "bio", UpdatedAt: stamp, Role: 1, Status: 0},
		Account: database.Account{HasLogo: true, UpdatedAt: stamp},
		Room:    database.Room{ID: 9, Name: "R", Type: "Rooms::Open", UpdatedAt: stamp},
		Origin:  "https://x.test", Stream: "stream",
		Platform: useragent.Platform{Desktop: true, Browser: "Chrome", OperatingSystem: "macOS"},
		Query:    "q", ReturnRoom: 4, RecentSearches: []string{"a", "b"},
	}
	baseline := recordedShellIdentity("room", base)
	mutations := []struct {
		name   string
		mutate func(*page)
	}{
		{"title", func(p *page) { p.Title = "Other" }},
		{"frame", func(p *page) { p.Frame = true }},
		{"reload", func(p *page) { p.Reload = true }},
		{"chat", func(p *page) { p.Chat = false }},
		{"screen", func(p *page) { p.Screen = "search" }},
		{"body class", func(p *page) { p.BodyClass = "signup" }},
		{"notice", func(p *page) { p.Notice = "saved" }},
		{"error", func(p *page) { p.Error = "failed" }},
		{"back path", func(p *page) { p.BackPath = "/rooms/9" }},
		{"version", func(p *page) { p.Version = "v2" }},
		{"vapid key", func(p *page) { p.VAPIDPublicKey = "other" }},
		{"custom styles", func(p *page) { p.CustomStyles = "<style>y</style>" }},
		{"user id", func(p *page) { p.User.ID = 4 }},
		{"user name", func(p *page) { p.User.Name = "Grace" }},
		{"user bio", func(p *page) { p.User.Bio = "other" }},
		{"user role", func(p *page) { p.User.Role = 0 }},
		{"user status", func(p *page) { p.User.Status = 1 }},
		{"user updated", func(p *page) { p.User.UpdatedAt = later }},
		{"account logo", func(p *page) { p.Account.HasLogo = false }},
		{"account updated", func(p *page) { p.Account.UpdatedAt = later }},
		{"room id", func(p *page) { p.Room.ID = 10 }},
		{"room name", func(p *page) { p.Room.Name = "Renamed" }},
		{"room type", func(p *page) { p.Room.Type = "Rooms::Direct" }},
		{"room updated", func(p *page) { p.Room.UpdatedAt = later }},
		{"invitation", func(p *page) { p.Invitation = true }},
		{"origin", func(p *page) { p.Origin = "https://y.test" }},
		{"stream", func(p *page) { p.Stream = "other" }},
		{"platform ios", func(p *page) { p.Platform.IOS = true }},
		{"platform android", func(p *page) { p.Platform.Android = true }},
		{"platform mac", func(p *page) { p.Platform.Mac = true }},
		{"platform windows", func(p *page) { p.Platform.Windows = true }},
		{"platform chrome", func(p *page) { p.Platform.Chrome = true }},
		{"platform firefox", func(p *page) { p.Platform.Firefox = true }},
		{"platform safari", func(p *page) { p.Platform.Safari = true }},
		{"platform edge", func(p *page) { p.Platform.Edge = true }},
		{"platform mobile", func(p *page) { p.Platform.Mobile = true }},
		{"platform desktop", func(p *page) { p.Platform.Desktop = false }},
		{"platform apple messages", func(p *page) { p.Platform.AppleMessages = true }},
		{"platform browser", func(p *page) { p.Platform.Browser = "Firefox" }},
		{"platform os", func(p *page) { p.Platform.OperatingSystem = "Windows" }},
		{"query", func(p *page) { p.Query = "other" }},
		{"return room", func(p *page) { p.ReturnRoom = 5 }},
		{"recent search", func(p *page) { p.RecentSearches = []string{"a", "c"} }},
		{"message count", func(p *page) { p.Messages = make([]messageView, 1) }},
	}
	for _, m := range mutations {
		p := base
		m.mutate(&p)
		if got := recordedShellIdentity("room", p); got == baseline {
			t.Errorf("%s does not change the shell identity", m.name)
		}
	}
	// These inputs are inserted per request or replaced by the message-list
	// piece, so they must not participate in the shell identity.
	for _, m := range []struct {
		name   string
		mutate func(*page)
	}{
		{"messages html marker", func(p *page) { p.MessagesHTML = "<x>" }},
		{"loaded at", func(p *page) { p.LoadedAt = "1" }},
	} {
		p := base
		m.mutate(&p)
		if got := recordedShellIdentity("room", p); got != baseline {
			t.Errorf("%s changed the shell identity", m.name)
		}
	}
	// The message list's bytes are keyed separately, so two same-length lists
	// share the shell identity.
	first := base
	first.Messages = []messageView{{Message: database.Message{ID: 1, UpdatedAt: stamp}}}
	second := base
	second.Messages = []messageView{{Message: database.Message{ID: 2, UpdatedAt: later}}}
	if recordedShellIdentity("room", first) != recordedShellIdentity("room", second) {
		t.Fatal("shell identity depends on message contents, not just the count")
	}
}

// TestRecordedAssemblyBufferPoisoning is the ownership proof for the pooled
// assembly buffer: every buffer the pool hands out is filled with 0xAA before
// two different pages are served concurrently, and each response must decode
// to its own content. A buffer not fully overwritten, or a response reporting
// capacity instead of length, would surface as poison bytes or as the other
// page's content.
func TestRecordedAssemblyBufferPoisoning(t *testing.T) {
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	app, server, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	open := rooms[0]
	second, err := app.DB.CreateRoom(ctx, user.ID, "Rooms::Closed", "Poison second", []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, open.ID, "poison-a", "<p>poison alpha marker</p>", "poison alpha marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, second.ID, "poison-b", "<p>poison beta marker</p>", "poison beta marker"); err != nil {
		t.Fatal(err)
	}
	// Poison the pool directly: any request that borrows one of these buffers
	// must overwrite it completely.
	for i := 0; i < 4; i++ {
		assembly := borrowAssemblyBuffer()
		buf := assembly.buf
		if cap(buf) == 0 {
			buf = make([]byte, 0, 64<<10)
		}
		buf = buf[:cap(buf)]
		for j := range buf {
			buf[j] = 0xAA
		}
		assembly.buf = buf
		assembly.release()
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	type pageCase struct {
		id           int64
		want, reject string
	}
	results := make(chan error, 2)
	for _, room := range []pageCase{
		{open.ID, "poison alpha marker", "poison beta marker"},
		{second.ID, "poison beta marker", "poison alpha marker"},
	} {
		room := room
		go func() {
			response, err := recordedFetch(client, server, fmt.Sprintf("/rooms/%d", room.id), "gzip", "", cookie)
			if err != nil {
				results <- err
				return
			}
			decoded, err := decodeRecordedErr(response)
			if err != nil {
				results <- err
				return
			}
			if response.status != 200 {
				results <- fmt.Errorf("status %d", response.status)
				return
			}
			if !bytes.Contains(decoded, []byte(room.want)) || bytes.Contains(decoded, []byte(room.reject)) {
				results <- fmt.Errorf("room %d body mixed page content", room.id)
				return
			}
			results <- nil
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// TestRecordedPiecesFlagParsing pins the CAMPFIRE_RECORDED_PIECES contract:
// false/0/off disable the piece path, on/true/1 (and unset) keep it on, and an
// unknown value keeps the default on while warning once at startup.
func TestRecordedPiecesFlagParsing(t *testing.T) {
	for _, c := range []struct {
		value   string
		enabled bool
		valid   bool
	}{
		{"", true, true},
		{"on", true, true},
		{"true", true, true},
		{"1", true, true},
		{"ON", true, true},
		{" off ", false, true},
		{"false", false, true},
		{"0", false, true},
		{"sometimes", true, false},
		{"no", true, false},
	} {
		enabled, valid := parseRecordedPieces(c.value)
		if enabled != c.enabled || valid != c.valid {
			t.Errorf("parseRecordedPieces(%q) = (%v, %v), want (%v, %v)", c.value, enabled, valid, c.enabled, c.valid)
		}
	}
	for _, value := range []string{"false", "0", "off"} {
		t.Setenv("CAMPFIRE_RECORDED_PIECES", value)
		app, _, _, _ := testApp(t)
		if app.recordedPieces {
			t.Fatalf("CAMPFIRE_RECORDED_PIECES=%q kept pieces on", value)
		}
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "sometimes")
	app, _, _, _ := testApp(t)
	if !app.recordedPieces {
		t.Fatal("unknown value disabled pieces")
	}
	if !strings.Contains(logs.String(), "invalid CAMPFIRE_RECORDED_PIECES") {
		t.Fatalf("unknown value did not warn: %q", logs.String())
	}
}

// TestRecordedPiecesShellSplitFallback covers the defensive path for a shell
// shape the splitter cannot use. The first request renders the shell, fails to
// split it, tombstones the identity and falls back to the legacy render; later
// requests read the tombstone and serve the legacy render without rendering
// the shell again (proved by removing the templates for the second request),
// incrementing the fallback counter each time but warning only once.
func TestRecordedPiecesShellSplitFallback(t *testing.T) {
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)

	app, _, cookie, user := testApp(t)
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(ctx, user.ID, rooms[0].ID, "split", "<p>split fallback marker</p>", "split fallback marker"); err != nil {
		t.Fatal(err)
	}
	// A room template that prints the message marker twice: the splitter
	// rejects repeated markers, the legacy writeRecorded still cuts at the
	// first one.
	broken := template.Must(app.templates.Clone())
	broken = template.Must(broken.New("room").Parse(`{{define "room"}}{{template "messages" .}}{{template "messages" .}}{{end}}`))
	app.templates = broken

	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", fmt.Sprintf("/rooms/%d", rooms[0].ID), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	first := request()
	if first.Code != 200 || !strings.Contains(first.Body.String(), "split fallback marker") {
		t.Fatalf("first request: %d", first.Code)
	}
	if got := app.recordedShellFallbacks.Load(); got != 1 {
		t.Fatalf("fallbacks after the first request = %d, want 1", got)
	}
	if got := app.recordedAssemblies.Load(); got != 0 {
		t.Fatalf("split failure assembled %d body/bodies", got)
	}
	// No template may be executed any more: the message list and the legacy
	// shell are cached and the identity is tombstoned, so a render attempt
	// would fail with 500.
	app.templates = template.New("empty")
	second := request()
	if second.Code != 200 {
		t.Fatalf("second request re-rendered: %d", second.Code)
	}
	if got := app.recordedShellFallbacks.Load(); got != 2 {
		t.Fatalf("fallbacks after the second request = %d, want 2", got)
	}
	if got := strings.Count(logs.String(), "recorded response served by the legacy renderer"); got != 1 {
		t.Fatalf("fallback warnings = %d, want 1", got)
	}
}
