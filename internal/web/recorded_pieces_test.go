package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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
	on, err := New(db, secrets, false, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "off")
	off, err := New(db, secrets, false, root)
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
	status   int
	etag     string
	encoding string
	vary     string
	body     []byte
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

// recordedFetch is the goroutine-safe form of recordedGet.
func recordedFetch(client *http.Client, server *httptest.Server, path, accept, ifNoneMatch string, cookie *http.Cookie) (recordedExchange, error) {
	request, err := http.NewRequest("GET", server.URL+path, nil)
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
	return recordedExchange{status: response.StatusCode, etag: response.Header.Get("ETag"), encoding: response.Header.Get("Content-Encoding"), vary: response.Header.Get("Vary"), body: body}, nil
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
// the piece path assembles one gzip member per piece (RFC 1952 multi-member)
// where front.Deflate emits a single member; ETag values differ for the room
// route by the documented loadedAt-free scheme.
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
	if bytes.Contains(decodeRecorded(t, roomResponse), []byte("edited marker")) {
		t.Fatal("room still serves the deleted message")
	}
	search = recordedGet(t, client, server, searchPath, "gzip", "", cookie)
	if bytes.Contains(decodeRecorded(t, search), []byte("edited marker")) {
		t.Fatal("search still serves the deleted message")
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

func TestRecordedCompressorPoolPoisoning(t *testing.T) {
	poison := recordedCompressors.Get().(*recordedCompressor)
	poison.buf.WriteString(strings.Repeat("POISON", 64))
	poison.writer.Reset(nil)
	recordedCompressors.Put(poison)

	first := compressGzip([]byte("first payload"))
	second := compressGzip([]byte("second payload"))
	for name, tc := range map[string]struct {
		member []byte
		want   string
	}{"first": {first, "first payload"}, "second": {second, "second payload"}} {
		reader, err := gzip.NewReader(bytes.NewReader(tc.member))
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
		{"gzip;q=0.5", false},
		{"gzip, identity", false},
		{"gzip, identity;q=0", true},
		{"identity", false},
		{"*", false},
		{"br", false},
		{"gzip;q=0, gzip;q=1", true},
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
