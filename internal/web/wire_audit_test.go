package web

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/front"
	"github.com/basecamp/once-campfire-go/internal/rails"
	"github.com/basecamp/once-campfire-go/internal/storage"
)

func TestPrecomposedRejectsRequestBody(t *testing.T) {
	for _, mode := range []string{"fixed", "chunked", "expect"} {
		t.Run(mode, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", strings.NewReader("body"))
			if mode == "chunked" {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			if mode == "expect" {
				r.Header.Set("Expect", "100-continue")
			}
			if precomposedEligible(r, http.Header{}, nil) {
				t.Fatal("body-bearing request eligible for precomposed response")
			}
		})
	}
}

func TestPublicDiskReplayRechecksExpiryAndDeletion(t *testing.T) {
	app, _, _, _, _, _ := testRecordedPair(t)
	base := app.DB.Now()
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	app.DB.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	content, ct := "media data", "text/plain"
	sum := md5.Sum([]byte(content))
	blob, err := app.Storage.Create(context.Background(), storage.Blob{Filename: "test.txt", ContentType: &ct, ByteSize: int64(len(content)), Checksum: base64.StdEncoding.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Storage.Upload(context.Background(), storage.DiskToken{Key: blob.Key, ContentType: blob.ContentType, ContentLength: blob.ByteSize, Checksum: blob.Checksum, ServiceName: blob.ServiceName}, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	path, err := app.Storage.DiskURL(blob, "inline")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(front.NewCache(8<<20, 1<<20).Handler(app))
	defer server.Close()
	client := recordedClient()
	defer client.CloseIdleConnections()
	fetch := func(path string) int { return recordedGet(t, client, server, path, "identity", "", nil).status }
	if got := fetch(path); got != 200 {
		t.Fatalf("fill: %d", got)
	}
	clock.Store(base.Add(6 * time.Minute).UnixNano())
	if got := fetch(path); got == 200 {
		t.Error("expired disk capability replayed")
	}
	fresh, err := app.Storage.DiskURL(blob, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if got := fetch(fresh); got != 200 {
		t.Fatalf("fresh fill: %d", got)
	}
	if err := app.Storage.Purge(context.Background(), blob.ID); err != nil {
		t.Fatal(err)
	}
	if got := fetch(fresh); got == 200 {
		t.Error("deleted media replayed")
	}
}

func TestPrecomposedUnreadBodyParity(t *testing.T) {
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	t.Setenv("CAMPFIRE_RECORDED_ZSTD", "on")
	app, _, _, _, cookie, user := testRecordedPair(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	room := rooms[0].ID
	if _, err := app.DB.CreateMessage(context.Background(), user.ID, room, "seed", "<p>recorded seed</p>", "recorded seed"); err != nil {
		t.Fatal(err)
	}
	var ownedCalls, stdCalls atomic.Int64
	wrap := func(calls *atomic.Int64) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/second" {
				calls.Add(1)
				io.WriteString(w, "SECOND")
				return
			}
			app.ServeHTTP(w, r)
		})
	}
	_, ownedAddr := serveLoop(t, wrap(&ownedCalls))
	std := httptest.NewServer(wrap(&stdCalls))
	defer std.Close()
	stdAddr := strings.TrimPrefix(std.URL, "http://")
	client := recordedClient()
	defer client.CloseIdleConnections()
	send := func(t *testing.T, addr, request string) []byte {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(4 * time.Second))
		if _, err := io.WriteString(c, request); err != nil {
			t.Fatal(err)
		}
		wire, err := io.ReadAll(c)
		if err != nil && !errors.Is(err, syscall.ECONNRESET) {
			t.Fatal(err)
		}
		return maskDate(wire)
	}
	for _, path := range []string{fmt.Sprintf("/rooms/%d", room), fmt.Sprintf("/rooms/%d/messages", room), "/searches?q=recorded", "/users/me/sidebar"} {
		for _, encoding := range []string{"identity", "gzip", "zstd, gzip;q=0.5"} {
			warm := recordedGet(t, client, std, path, encoding, "", cookie)
			for _, conditional := range []bool{false, true} {
				for _, bodySize := range []int{1024, 300 << 10} {
					for _, mode := range []string{"fixed", "chunked", "expect"} {
						t.Run(fmt.Sprintf("%s/%s/%v/%s/%d", path, encoding, conditional, mode, bodySize), func(t *testing.T) {
							body := strings.Repeat("x", bodySize)
							framing := fmt.Sprintf("Content-Length: %d\r\n", len(body))
							if mode == "chunked" {
								framing = "Transfer-Encoding: chunked\r\n"
								body = fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(body), body)
							}
							if mode == "expect" {
								framing += "Expect: 100-continue\r\n"
							}
							condition := ""
							if conditional {
								condition = "If-None-Match: " + warm.etag + "\r\n"
							}
							request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: campfire.test\r\nCookie: session_token=%s; last_room=%d\r\nAccept-Encoding: %s\r\n%s%s\r\n%sGET /second HTTP/1.1\r\nHost: campfire.test\r\nConnection: close\r\n\r\n", path, cookie.Value, room, encoding, condition, framing, body)
							ownedCalls.Store(0)
							stdCalls.Store(0)
							got, want := send(t, ownedAddr, request), send(t, stdAddr, request)
							if !bytes.Equal(got, want) {
								t.Errorf("wire mismatch: owned=%q standard=%q", got, want)
							}
							if ownedCalls.Load() != stdCalls.Load() {
								t.Errorf("second handler: owned=%d standard=%d", ownedCalls.Load(), stdCalls.Load())
							}
						})
					}
				}
			}
		}
	}
}

func TestContractOnlyRecognition(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "POST", "PATCH", "DELETE"} {
		for _, path := range []string{"/rooms/1", "/rooms/1/messages", "/searches", "/users/a/avatar", "/users/%FF/avatar", "/users/%zz/avatar", "/missing"} {
			want, _, wantErr := recognize(method, path)
			got, gotErr := recognizeContract(method, path)
			if got != want || (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("%s %s: contract-only mismatch", method, path)
			}
		}
	}
	full := testing.AllocsPerRun(100, func() { compiledRecognize("GET", "/rooms/1") })
	contract := testing.AllocsPerRun(100, func() { compiledRecognizeMode("GET", "/rooms/1", false) })
	if contract >= full {
		t.Fatalf("discarded param-map allocations remain: contract=%g full=%g", contract, full)
	}
}

func TestFramedSecurityHeadersStayOutOfMap(t *testing.T) {
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	app, _, _, _, cookie, user := testRecordedPair(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(context.Background(), user.ID, rooms[0].ID, "seed", "<p>recorded seed</p>", "recorded seed"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", fmt.Sprintf("/rooms/%d", rooms[0].ID), nil)
		r.AddCookie(cookie)
		r.AddCookie(&http.Cookie{Name: "last_room", Value: fmt.Sprint(rooms[0].ID)})
		r.Header.Set("Accept-Encoding", "identity")
		w := &benchPrecomposedWriter{header: make(http.Header)}
		app.ServeHTTP(w, r)
		if w.status != 200 {
			t.Fatalf("not framed: %d", w.status)
		}
		for _, key := range []string{"X-Version", "X-Rev", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "X-XSS-Protection", "X-Permitted-Cross-Domain-Policies"} {
			if _, ok := w.header[http.CanonicalHeaderKey(key)]; ok {
				t.Errorf("fixed header %s inserted into live map", key)
			}
		}
	}
}

func TestFramingGateFallbackRetainsSecurityHeaders(t *testing.T) {
	app, _, _, _, _, _ := testRecordedPair(t)
	for _, proxy := range []string{"", "X-Forwarded-For: 198.51.100.20\r\nClient-IP: 203.0.113.1\r\n"} {
		t.Run(fmt.Sprintf("proxy=%t", proxy != ""), func(t *testing.T) {
			var wires [2][]byte
			for i, enabled := range []bool{false, true} {
				var handler http.Handler = app
				if !enabled {
					handler = hidePrecomposed(handler)
				}
				_, addr := serveLoop(t, handler)
				wires[i] = auditWire(t, addr, "GET /up HTTP/1.1\r\nHost: campfire.test\r\nConnection: close\r\n"+proxy+"\r\n")
			}
			if !bytes.Equal(wires[0], wires[1]) {
				t.Fatalf("unframed=%q framed=%q", wires[0], wires[1])
			}
			if proxy != "" && bytes.Contains(wires[0], []byte("X-Version:")) {
				t.Fatalf("early error acquired application headers: %q", wires[0])
			}
		})
	}
}

func auditWire(t *testing.T, addr, request string) []byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	wire, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return maskDate(wire)
}

func TestRejectedGenerationSkipsAdmissionWork(t *testing.T) {
	app, _, _, _, _, _ := testRecordedPair(t)
	info := &requestInfo{host: "example.com", origin: "http://example.com", generationChecked: true, generationCurrent: false, response: &responseRound{version: 1, user: 1, keyed: true}}
	r := httptest.NewRequest("GET", "/rooms/1", nil)
	r = r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, info))
	w := borrowResponseBuffer(httptest.NewRecorder(), nil)
	defer releaseResponseBuffer(w)
	w.status = 200
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte("a rendered response"))
	if allocs := testing.AllocsPerRun(100, func() { app.cacheResponse(r, w) }); allocs != 0 {
		t.Fatalf("rejected generation did admission work: %g allocations", allocs)
	}
}

func TestPublicAvatarReplayRechecksSession(t *testing.T) {
	app, _, _, _, cookie, user := testRecordedPair(t)
	server := httptest.NewServer(front.NewCache(8<<20, 1<<20).Handler(app))
	defer server.Close()
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := signedAvatar(app.Secrets, user.ID, time.Time{})
	if got := recordedGet(t, client, server, path, "identity", "", cookie); got.status != 200 {
		t.Fatalf("fill: %d", got.status)
	}
	if got := recordedGet(t, client, server, path, "identity", "", nil); got.status == 200 {
		t.Error("unauthenticated avatar replay")
	}
	token, err := app.verifiedSessionToken(rails.UnescapeCookie(cookie.Value), app.DB.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.DeleteSession(context.Background(), token, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := recordedGet(t, client, server, path, "identity", "", cookie); got.status == 200 {
		t.Error("revoked session avatar replay")
	}
}

func TestRecordedZstdRejectedFallback(t *testing.T) {
	t.Setenv("CAMPFIRE_RECORDED_ZSTD", "on")
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	app, _, _, _, cookie, user := testRecordedPair(t)
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(context.Background(), user.ID, rooms[0].ID, "seed", "<p>recorded seed</p>", "recorded seed"); err != nil {
		t.Fatal(err)
	}
	var preparations atomic.Int64
	app.beforeRecordedPrepare = func() { preparations.Add(1) }
	for _, chain := range []func(http.Handler) http.Handler{publicReplayChain, productionReplayChain} {
		server := httptest.NewServer(chain(app))
		defer server.Close()
		client := recordedClient()
		defer client.CloseIdleConnections()
		for _, method := range []string{"GET", "HEAD"} {
			for _, accept := range []string{"zstd;q=1, gzip;q=0, identity;q=0", "zstd;q=1, gzip;q=0, identity;q=0.5"} {
				for i := 0; i < 2; i++ {
					before := preparations.Load()
					got, err := recordedFetchMethod(client, server, method, "/searches?q=recorded", accept, "", cookie)
					if err != nil {
						t.Fatal(err)
					}
					if got.encoding == "gzip" {
						t.Errorf("%s %s: forbidden gzip", method, accept)
					}
					if strings.HasSuffix(accept, "identity;q=0") && got.status != 406 {
						t.Errorf("%s: status=%d, want 406", method, got.status)
					}
					if strings.HasSuffix(accept, "identity;q=0") && preparations.Load() != before {
						t.Errorf("%s rejected coding reached compression/admission preparation %d times", method, preparations.Load()-before)
					}
					if strings.HasSuffix(accept, "identity;q=0.5") && got.status != 200 {
						t.Errorf("%s: identity accepted, status=%d", method, got.status)
					}
				}
			}
		}
	}
}

func TestRecordedZstdMissingFramesRejectedFallback(t *testing.T) {
	t.Setenv("CAMPFIRE_RESPONSE_CACHE_MB", "0")
	app, warmServer, _, cookie, user := testSingleApp(t, "CAMPFIRE_RECORDED_ZSTD=off")
	rooms, err := app.DB.Rooms(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateMessage(context.Background(), user.ID, rooms[0].ID, "missing", "<p>recorded seed</p>", "recorded seed"); err != nil {
		t.Fatal(err)
	}
	client := recordedClient()
	defer client.CloseIdleConnections()
	path := fmt.Sprintf("/rooms/%d", rooms[0].ID)
	recordedGet(t, client, warmServer, path, "gzip", "", cookie)
	// Fill without zstd frames before enabling the recorded selector.
	app.zstdPieces = true
	server := httptest.NewServer(publicReplayChain(app))
	defer server.Close()
	for _, method := range []string{"GET", "HEAD"} {
		for _, accept := range []string{"zstd, gzip;q=0, identity;q=0", "zstd, gzip;q=0, identity;q=0.5"} {
			got, err := recordedFetchMethod(client, server, method, path, accept, "", cookie)
			if err != nil {
				t.Fatal(err)
			}
			want := 200
			if strings.HasSuffix(accept, "identity;q=0") {
				want = 406
			}
			if got.encoding == "gzip" || got.status != want {
				t.Errorf("%s %s: encoding=%q status=%d, want status=%d without gzip", method, accept, got.encoding, got.status, want)
			}
		}
	}
}
