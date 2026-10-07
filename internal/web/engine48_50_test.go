package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/fastserve"
	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/httpcompat"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

// ---------------------------------------------------------------------------
// ENGINE-50: negotiation and zstd members.

// corpusAcceptEncoding is the shared header corpus: the httpcompat pin cases,
// wildcard and q= forms, duplicates, and the zstd-relevant ones.
var corpusAcceptEncoding = []string{
	"", "gzip", "identity", "gzip, identity", "identity;q=0", "gzip;q=0",
	"gzip;q=0,identity;q=0", "gzip;q=0.5", "gzip;q=0.5, identity;q=1",
	"*", "*;q=1", "*;q=0", "gzip, *", "gzip;q=1.0", "GZIP", "gzip, gzip",
	"gzip;q=0, gzip;q=1", "zstd", "gzip, zstd", "zstd, gzip",
	"gzip;q=0, zstd;q=1", "gzip;q=0.5, zstd;q=1", "zstd;q=0",
	"zstd;q=0.5, identity;q=1", "identity;q=0, gzip;q=0, zstd;q=0",
	"br, zstd", "br;q=0.5, zstd;q=1, gzip;q=0.9", "gzip;q=0.3, zstd;q=0.2",
}

// TestClientContentEncodingMatchesHTTPCompat pins the ENGINE-50 selector's
// off mode: with zstd disabled it must answer exactly what the shared
// httpcompat selector answers, byte for byte, so the gzip-only world is
// unchanged.
func TestClientContentEncodingMatchesHTTPCompat(t *testing.T) {
	for _, header := range corpusAcceptEncoding {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", header)
		want := httpcompat.Encoding(header)
		got := clientContentEncoding(r, false)
		if got != want {
			t.Errorf("zstd off, header %q: got %q, want %q", header, got, want)
		}
	}
}

// TestClientContentEncodingZstd pins the zstd-on decisions: existing gzip
// clients keep gzip, only clients that accept zstd over gzip change.
func TestClientContentEncodingZstd(t *testing.T) {
	cases := []struct{ header, want string }{
		{"", "identity"},
		{"gzip", "gzip"},
		{"gzip, zstd", "gzip"},
		{"zstd, gzip", "gzip"},
		{"zstd", "zstd"},
		{"gzip;q=0, zstd", "zstd"},
		{"gzip;q=0.5, zstd;q=1", "zstd"},
		{"gzip;q=0.3, zstd;q=0.2", "gzip"},
		{"zstd;q=0", "identity"},
		{"identity;q=0, gzip;q=0, zstd;q=0", ""},
		{"*", "gzip"},
		{"*;q=0", ""},
		{"br, zstd", "zstd"},
		{"br;q=1, zstd;q=0.5", "zstd"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			r.Header.Set("Accept-Encoding", c.header)
		}
		if got := clientContentEncoding(r, true); got != c.want {
			t.Errorf("header %q: got %q, want %q", c.header, got, c.want)
		}
	}
}

// decodeZstd decodes a (multi-frame) zstd body with the system CLI, the same
// decoder the zstd package tests use.
func decodeZstd(t *testing.T, frames []byte) []byte {
	t.Helper()
	command := exec.Command("zstd", "-d", "-q", "-c")
	command.Stdin = bytes.NewReader(frames)
	decoded, err := command.Output()
	if err != nil {
		t.Fatalf("zstd decode: %v", err)
	}
	return decoded
}

// TestRecordedZstdResponse serves the room page to a zstd-accepting client:
// Content-Encoding zstd, the multi-frame body decodes to the identity body,
// and a server with zstd members off serves the same request gzip/identity as
// before (byte parity with the pre-engine negotiation).
func TestRecordedZstdResponse(t *testing.T) {
	on, _, onServer, offServer, cookie, user := testRecordedPair(t)
	ctx := context.Background()
	rooms, err := on.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	if _, err := on.DB.CreateMessage(ctx, user.ID, room.ID, "z1", "<p>zstd body</p>", "zstd body"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := fmt.Sprintf("/rooms/%d", room.ID)

	// zstd server: the client that accepts zstd only gets zstd.
	zstdEx := recordedGet(t, client, onServer, path, "zstd", "", cookie)
	if zstdEx.encoding != "zstd" {
		t.Fatalf("zstd client got Content-Encoding %q", zstdEx.encoding)
	}
	if !strings.Contains(strings.ToLower(zstdEx.vary), "accept-encoding") {
		t.Fatalf("Vary %q lacks Accept-Encoding", zstdEx.vary)
	}
	identity := recordedGet(t, client, onServer, path, "identity", "", cookie)
	if !bytes.Equal(decodeZstd(t, zstdEx.body), decodeRecorded(t, identity)) {
		t.Fatalf("zstd body and identity body differ")
	}

	// Browsers send gzip first: still gzip.
	gzipEx := recordedGet(t, client, onServer, path, "gzip, zstd", "", cookie)
	if gzipEx.encoding != "gzip" {
		t.Fatalf("gzip-priority client got Content-Encoding %q", gzipEx.encoding)
	}

	// zstd-off server: the same zstd-only client gets identity (httpcompat's
	// answer), byte-identical to the pre-engine negotiation.
	offEx := recordedGet(t, client, offServer, path, "zstd", "", cookie)
	// httpcompat answers "identity" for a zstd-only client, so the legacy
	// path serves identity (no Content-Encoding header).
	if offEx.encoding != "" {
		t.Fatalf("zstd-off server answered %q for a zstd client", offEx.encoding)
	}
	if !bytes.Equal(decodeRecorded(t, offEx), decodeRecorded(t, identity)) {
		t.Fatalf("zstd-off identity body differs from the zstd server's identity body")
	}
}

// TestRecordedZstdDegradeToGzip pins the mixed-flag cache: pieces filled
// without zstd members serve a zstd client gzip, never an empty body.
func TestRecordedZstdDegradeToGzip(t *testing.T) {
	app, server, _, cookie, user := testSingleApp(t, "CAMPFIRE_RECORDED_ZSTD=off")
	client := recordedClient()
	defer client.CloseIdleConnections()
	ctx := context.Background()
	rooms, err := app.DB.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	if _, err := app.DB.CreateMessage(ctx, user.ID, room.ID, "d1", "<p>degrade</p>", "degrade"); err != nil {
		t.Fatal(err)
	}
	// Fill the cache gzip-only, then turn zstd on like a flag change would.
	path := fmt.Sprintf("/rooms/%d", room.ID)
	recordedGet(t, client, server, path, "gzip", "", cookie)
	app.zstdPieces = true
	exchange := recordedGet(t, client, server, path, "zstd", "", cookie)
	if exchange.encoding != "gzip" {
		t.Fatalf("zstd-less cache answered %q for a zstd client, want gzip", exchange.encoding)
	}
	reader, err := gzip.NewReader(bytes.NewReader(exchange.body))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "degrade") {
		t.Fatalf("degraded body lost the message")
	}
}

// testSingleApp builds one full server with front.Deflate (used by tests that
// override flags on a single app).
func testSingleApp(t *testing.T, env ...string) (*Server, *httptest.Server, *http.Server, *http.Cookie, database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		t.Setenv(key, value)
	}
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
	app, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	server := httptest.NewServer(front.Deflate(app))
	t.Cleanup(server.Close)
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
	return app, server, server.Config, &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}, user
}

// TestRecordedGzipLevelSizes pins the ENGINE-50 fill level: level 9 members
// are smaller than level 6 on page-like HTML, and both decode to the raw
// bytes (so the wire is unchanged apart from size).
func TestRecordedGzipLevelSizes(t *testing.T) {
	raw := benchmarkMarkup(64<<10, "level comparison payload")
	atLevel6 := compressGzipLevel(raw, 6)
	atLevel9 := compressGzipLevel(raw, 9)
	if len(atLevel9) >= len(atLevel6) {
		t.Fatalf("level 9 (%d bytes) not smaller than level 6 (%d)", len(atLevel9), len(atLevel6))
	}
	for _, member := range [][]byte{atLevel6, atLevel9} {
		reader, err := gzip.NewReader(bytes.NewReader(member))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, raw) {
			t.Fatalf("level decode mismatch: %d vs %d bytes", len(decoded), len(raw))
		}
	}
}

// ---------------------------------------------------------------------------
// ENGINE-48: arena and pool poisoning.

// TestRequestArenaPoisoning pins the overwrite contract: the release fills
// the handed-out range with the poison byte, so a carve that reads a byte it
// did not write sees 0xAA — never a previous request's data. The test drives
// one block directly (poisonAndReset is the release's internal hook, so the
// pool's shared state cannot interleave a different block into the test).
func TestRequestArenaPoisoning(t *testing.T) {
	arena := &requestArena{buf: make([]byte, 0, arenaInitial), poison: true}
	first := arena.carve(64)
	copy(first, "previous request secret")
	arena.carve(16) // a second carve proves the poison covers the whole used range
	arena.poisonAndReset()

	next := arena.carveBlock(64)
	data := next[:64]
	if data[0] != 0xAA {
		t.Fatalf("carve returned 0x%02x, want poison 0xaa: an unpoisoned block served a previous carve", data[0])
	}
	for i := range data {
		if data[i] == 'p' {
			t.Fatalf("carve served the previous request's bytes at offset %d", i)
		}
	}
}

// TestResponseBufferPoolReset pins the pooled writer struct contract: a
// released buffer comes back with every field reset, so a stale arena or
// precomposed state can never leak into the next request.
func TestResponseBufferPoolReset(t *testing.T) {
	made := false
	origNew := responseBufferPool.New
	responseBufferPool.New = func() any { made = true; return origNew() }
	defer func() { responseBufferPool.New = origNew }()
	_ = made

	b := borrowResponseBuffer(&benchResponseWriter{header: http.Header{}}, borrowRequestArena())
	b.status = 200
	b.encoded = []byte("stale")
	b.precomposed = true
	b.recordedStatus = 304
	releaseResponseBuffer(b)

	again := borrowResponseBuffer(&benchResponseWriter{header: http.Header{}}, nil)
	defer releaseResponseBuffer(again)
	if again.status != 0 || again.encoded != nil || again.precomposed || again.recordedStatus != 0 || again.arena != nil || again.body != nil {
		t.Fatalf("pooled responseBuffer not reset: %+v", again)
	}
}

// ---------------------------------------------------------------------------
// ENGINE-49: precomposed framing head parity through the owned writer.

// testFramedPair builds two full apps over one database — the precomposed
// path on and off — each served by a real fastserve loop, so the emitted
// wire bytes can be compared verbatim.
func testFramedPair(t *testing.T, framing, arena string) (db *database.DB, onAddr, offAddr string, cookie *http.Cookie, user database.User) {
	t.Helper()
	t.Setenv("CAMPFIRE_FROZEN_TIME", "2026-01-02T03:04:05Z")
	t.Setenv("CAMPFIRE_RECORDED_PIECES", "on")
	t.Setenv("CAMPFIRE_PRECOMPOSED_FRAMING", framing)
	t.Setenv("CAMPFIRE_REQUEST_ARENA", arena)
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
	on, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(on.Close)
	t.Setenv("CAMPFIRE_PRECOMPOSED_FRAMING", "off")
	t.Setenv("CAMPFIRE_REQUEST_ARENA", "off")
	off, err := New(db, secrets, false, filepath.Join(root, "test.sqlite3"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	t.Setenv("CAMPFIRE_PRECOMPOSED_FRAMING", "")
	t.Setenv("CAMPFIRE_REQUEST_ARENA", "")
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
	cookie = &http.Cookie{Name: "session_token", Value: rails.EscapeCookie(signed)}
	_, onAddr = serveLoop(t, front.Deflate(on))
	_, offAddr = serveLoop(t, front.Deflate(off))
	return
}

// serveLoop runs a fastserve loop on an ephemeral port and returns it with
// its address.
func serveLoop(t *testing.T, handler http.Handler) (*fastserve.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := fastserve.New(handler)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	go srv.Serve(listener)
	return srv, listener.Addr().String()
}

// rawFetch reads one raw HTTP/1.1 exchange from addr verbatim. extraCookies
// are additional cookie name=value pairs (the room route's last_room cookie
// keeps a returning browser on the framed path).
func rawFetch(t *testing.T, addr, host, method, path, accept, ifNoneMatch string, cookie *http.Cookie, extraCookies ...string) ([]byte, int) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var request strings.Builder
	fmt.Fprintf(&request, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, path, host)
	if accept != "" {
		fmt.Fprintf(&request, "Accept-Encoding: %s\r\n", accept)
	}
	if ifNoneMatch != "" {
		fmt.Fprintf(&request, "If-None-Match: %s\r\n", ifNoneMatch)
	}
	if cookie != nil {
		fmt.Fprint(&request, "Cookie: session_token="+cookie.Value)
		for _, extra := range extraCookies {
			fmt.Fprint(&request, "; "+extra)
		}
		request.WriteString("\r\n")
	}
	request.WriteString("\r\n")
	if _, err := conn.Write([]byte(request.String())); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	lines := bytes.Split(head, []byte("\r\n"))
	status := 0
	fmt.Sscanf(string(lines[0]), "HTTP/1.1 %d", &status)
	return raw, status
}

// maskDate drops the Date line so two wall-clock-separated exchanges compare
// byte for byte on everything else.
func maskDate(raw []byte) []byte {
	lines := bytes.Split(raw, []byte("\r\n"))
	out := lines[:0:0]
	for _, line := range lines {
		if !bytes.HasPrefix(line, []byte("Date: ")) {
			out = append(out, line)
		}
	}
	return bytes.Join(out, []byte("\r\n"))
}

// TestRecordedFramingWireParity is the ENGINE-49 head-parity contract: the
// framed and map paths emit byte-identical status lines, header blocks and
// bodies over the real fastserve writer, for gzip/identity, GET/HEAD,
// 200/304, anchors and the messages page.
func TestRecordedFramingWireParity(t *testing.T) {
	db, onAddr, offAddr, cookie, user := testFramedPair(t, "on", "on")
	ctx := context.Background()
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	var anchor int64
	for i := 0; i < 3; i++ {
		message, err := db.CreateMessage(ctx, user.ID, room.ID, fmt.Sprintf("parity-%d", i), fmt.Sprintf("<p>parity seed %d</p>", i), fmt.Sprintf("parity seed %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if anchor == 0 {
			anchor = message.ID
		}
	}
	path := fmt.Sprintf("/rooms/%d", room.ID)
	messagesPath := fmt.Sprintf("/rooms/%d/messages", room.ID)

	// Warm both caches with identical fills, then compare the second
	// exchanges byte for byte. The ETag must also match: both sides derive
	// it from the same content keys.
	warm := func(addr string) string {
		raw, status := rawFetch(t, addr, "campfire.test", "GET", path, "gzip", "", cookie, "last_room="+strconv.FormatInt(room.ID, 10))
		if status != 200 {
			t.Fatalf("warm: status %d", status)
		}
		head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
		for _, line := range bytes.Split(head, []byte("\r\n")) {
			if bytes.HasPrefix(line, []byte("Etag: ")) {
				return string(bytes.TrimPrefix(line, []byte("Etag: ")))
			}
		}
		t.Fatal("no ETag on warm response")
		return ""
	}
	etag := warm(onAddr)
	if offETag := warm(offAddr); etag != offETag {
		t.Fatalf("warm ETags differ: %q vs %q", etag, offETag)
	}

	cases := []struct {
		label, method, path, accept, ifNoneMatch string
	}{
		{"gzip", "GET", path, "gzip", ""},
		{"identity", "GET", path, "identity", ""},
		{"wildcard", "GET", path, "*", ""},
		{"gzip and identity", "GET", path, "gzip, identity", ""},
		{"gzip refused", "GET", path, "gzip;q=0", ""},
		{"head gzip", "HEAD", path, "gzip", ""},
		{"head identity", "HEAD", path, "identity", ""},
		{"messages gzip", "GET", messagesPath, "gzip", ""},
		{"messages identity", "GET", messagesPath, "identity", ""},
		{"fresh gzip", "GET", path, "gzip", etag},
		{"fresh identity", "GET", path, "identity", etag},
		{"anchor gzip", "GET", fmt.Sprintf("/rooms/%d/@%d", room.ID, anchor), "gzip", ""},
	}
	lastRoom := "last_room=" + strconv.FormatInt(room.ID, 10)
	for _, c := range cases {
		rawOn, statusOn := rawFetch(t, onAddr, "campfire.test", c.method, c.path, c.accept, c.ifNoneMatch, cookie, lastRoom)
		rawOff, statusOff := rawFetch(t, offAddr, "campfire.test", c.method, c.path, c.accept, c.ifNoneMatch, cookie, lastRoom)
		if statusOn != statusOff {
			t.Fatalf("%s: status %d vs %d", c.label, statusOn, statusOff)
		}
		if !bytes.Equal(maskDate(rawOn), maskDate(rawOff)) {
			a, b := maskDate(rawOn), maskDate(rawOff)
			at := 0
			for at < len(a) && at < len(b) && a[at] == b[at] {
				at++
			}
			os.WriteFile("/tmp/opencode/parity.on", rawOn, 0o644)
			os.WriteFile("/tmp/opencode/parity.off", rawOff, 0o644)
			lo := max(0, at-40)
			t.Fatalf("%s: wire differs at offset %d\n--- framed ---\n%q\n--- map ---\n%q", c.label, at, a[lo:min(len(a), at+40)], b[lo:min(len(b), at+40)])
		}
		if c.label == "messages gzip" {
			t.Logf("messages gzip raw sizes: %d vs %d", len(rawOn), len(rawOff))
		}
		t.Logf("%s OK (%d bytes)", c.label, len(rawOn))
	}
}

// TestRecordedFramingFlagsOffParity builds the framed pair with the flags
// off on both sides and asserts the two loops agree byte for byte (the map
// path is what the pre-flag tree emitted).
func TestRecordedFramingFlagsOffParity(t *testing.T) {
	db, onAddr, offAddr, cookie, user := testFramedPair(t, "off", "off")
	ctx := context.Background()
	rooms, err := db.Rooms(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0]
	if _, err := db.CreateMessage(ctx, user.ID, room.ID, "off1", "<p>off parity</p>", "off parity"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rooms/%d", room.ID)
	rawOn, statusOn := rawFetch(t, onAddr, "campfire.test", "GET", path, "gzip", "", cookie)
	rawOff, statusOff := rawFetch(t, offAddr, "campfire.test", "GET", path, "gzip", "", cookie)
	if statusOn != statusOff {
		t.Fatalf("status %d vs %d", statusOn, statusOff)
	}
	if !bytes.Equal(maskDate(rawOn), maskDate(rawOff)) {
		t.Fatalf("flags-off loops disagree:\n%q\n%q", maskDate(rawOn), maskDate(rawOff))
	}
}

// TestRequestArenaIsolation runs two sequential requests over one pooled
// arena: the second request's carves start after the first's, and the
// release reset makes the block reusable without any byte aliasing between
// the two requests.
func TestRequestArenaIsolation(t *testing.T) {
	arena := borrowRequestArena()
	defer releaseRequestArena(arena)
	a := arena.carve(8)
	copy(a, "first!!!")
	b := arena.carveBlock(16)
	b = append(b, "SECOND-BLOCK----"...) // carveBlock is appendable; append, don't copy
	if string(a) != "first!!!" {
		t.Fatalf("first carve corrupted: %q", a)
	}
	if string(b[:16]) != "SECOND-BLOCK----" {
		t.Fatalf("second carve corrupted: %q", b[:16])
	}
}

// TestRecordedNewFlagsParsing pins the ENGINE-48/49/50 flag gates: each new
// switch accepts the on/off shapes and keeps the default on an unrecognised
// value.
func TestRecordedNewFlagsParsing(t *testing.T) {
	for _, flag := range []string{"CAMPFIRE_REQUEST_ARENA", "CAMPFIRE_PRECOMPOSED_FRAMING", "CAMPFIRE_RECORDED_ZSTD", "CAMPFIRE_READ_CACHE"} {
		for _, value := range []string{"", "on", "true", "1", "off", "false", "0"} {
			enabled, valid := parseRecordedPieces(value)
			if !valid {
				t.Errorf("%s=%q: invalid", flag, value)
			}
			want := value != "off" && value != "false" && value != "0"
			if enabled != want {
				t.Errorf("%s=%q: enabled %v, want %v", flag, value, enabled, want)
			}
		}
		if enabled, valid := parseRecordedPieces("sometimes"); !enabled || valid {
			t.Errorf("%s=sometimes: (%v, %v), want (true, false)", flag, enabled, valid)
		}
	}
	for _, value := range []string{"", "6", "9", "1"} {
		if _, err := strconv.Atoi(value); err != nil && value != "" {
			t.Errorf("CAMPFIRE_RECORDED_GZIP_LEVEL=%q: %v", value, err)
		}
	}
}
