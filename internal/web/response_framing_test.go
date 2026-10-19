package web

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/front"
)

// The wire-framing contract: a fresh map-path response leaves Content-Length
// to the server (one body write, so a small body is fixed-length and a large
// body is chunked), and a response-cache hit must reproduce exactly that shape
// rather than inventing an explicit length. The baseline is pinned absolutely
// here — not by comparing two copies of the same build — because a global
// framing change would move both copies together and hide the regression.

// frameServer starts a raw HTTP/1.1 server for handler and returns its address.
func frameServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	})
	go server.Serve(listener)
	return listener.Addr().String()
}

// frameExchange dials addr and returns the raw response bytes verbatim.
func frameExchange(t *testing.T, addr, method string) []byte {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(method + " / HTTP/1.1\r\nHost: campfire.test\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// frameBody decodes the raw exchange's body (chunked or fixed-length).
func frameBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	_, rest, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		t.Fatalf("no header/body split:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("Transfer-Encoding: chunked")) {
		return rest
	}
	var out []byte
	for {
		line, after, ok := bytes.Cut(rest, []byte("\r\n"))
		if !ok {
			t.Fatalf("truncated chunk header:\n%s", raw)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(line)), 16, 64)
		if err != nil {
			t.Fatalf("bad chunk size %q", line)
		}
		if size == 0 {
			return out
		}
		if int64(len(after)) < size+2 {
			t.Fatalf("truncated chunk body:\n%s", raw)
		}
		out = append(out, after[:size]...)
		rest = after[size+2:]
	}
}

// TestResponseFramingBaseline pins the fresh map-path framing absolutely and
// requires the cached hit to reproduce it: a large identity body is chunked
// with no Content-Length, a small body is fixed-length, and HEAD carries
// neither. The cached hit is driven through serveCached so the framing
// decision (explicitLength=false) is the one admission records for a map-path
// render.
func TestResponseFramingBaseline(t *testing.T) {
	large := bytes.Repeat([]byte("x"), 22632)
	small := bytes.Repeat([]byte("y"), 100)

	fresh := func(payload []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b := &responseBuffer{ResponseWriter: w}
			_, _ = b.Write(payload)
			b.finish(r)
		}
	}
	hit := func(payload []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b := &responseBuffer{ResponseWriter: w}
			entry := &cachedResponse{
				body:   payload,
				header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
			}
			(&Server{}).serveCached(entry, b, r)
			b.finish(r)
		}
	}

	for _, c := range []struct {
		name        string
		method      string
		payload     []byte
		wantChunked bool
		wantLength  int // -1 when Content-Length must be absent
	}{
		{"large-get", "GET", large, true, -1},
		{"small-get", "GET", small, false, len(small)},
		{"large-head", "HEAD", large, false, -1},
		{"small-head", "HEAD", small, false, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, mode := range []struct {
				name    string
				handler http.HandlerFunc
			}{{"fresh", fresh(c.payload)}, {"hit", hit(c.payload)}} {
				addr := frameServer(t, mode.handler)
				raw := frameExchange(t, addr, c.method)
				head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
				chunked := bytes.Contains(head, []byte("Transfer-Encoding: chunked"))
				if chunked != c.wantChunked {
					t.Fatalf("%s %s: chunked=%v, want %v\n%s", c.name, mode.name, chunked, c.wantChunked, raw)
				}
				got := headerValue(head, "Content-Length")
				if c.wantLength < 0 {
					if got != "" {
						t.Fatalf("%s %s: Content-Length %q, want absent\n%s", c.name, mode.name, got, raw)
					}
				} else if got != strconv.Itoa(c.wantLength) {
					t.Fatalf("%s %s: Content-Length %q, want %d\n%s", c.name, mode.name, got, c.wantLength, raw)
				}
				if c.method == "GET" {
					if body := frameBody(t, raw); !bytes.Equal(body, c.payload) {
						t.Fatalf("%s %s: body %d bytes, want %d", c.name, mode.name, len(body), len(c.payload))
					}
				} else if body := frameBody(t, raw); len(body) != 0 {
					t.Fatalf("%s %s: HEAD returned %d body bytes", c.name, mode.name, len(body))
				}
			}
		})
	}
}

// headerValue returns the value of one header line from a raw header block.
func headerValue(head []byte, name string) string {
	prefix := name + ": "
	for _, line := range strings.Split(string(head), "\r\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// TestResponseCacheEmptySearchFraming is the baseline-versus-candidate wire
// test at the app level: an empty-result identity search renders on the map
// path, so the fresh response (cache disabled) is chunked with no
// Content-Length, and the cached hit (cache enabled) must emit the identical
// raw exchange. The hit counter is asserted so a non-engaging run cannot pass.
func TestResponseCacheEmptySearchFraming(t *testing.T) {
	on, off, _, _, cookie, _ := testResponseCachePair(t)
	_, onAddr := serveLoop(t, front.Deflate(on))
	_, offAddr := serveLoop(t, front.Deflate(off))
	const path = "/searches?q=no-such-term-at-all"

	// Fill the on-side cache, then capture the fresh and hit exchanges.
	rawFetch(t, onAddr, "campfire.test", "GET", path, "identity", "", cookie)
	hits0, _ := on.responses.counters()
	freshRaw, freshStatus := rawFetch(t, offAddr, "campfire.test", "GET", path, "identity", "", cookie)
	hitRaw, hitStatus := rawFetch(t, onAddr, "campfire.test", "GET", path, "identity", "", cookie)
	hits1, misses1 := on.responses.counters()
	if freshStatus != 200 || hitStatus != 200 {
		t.Fatalf("status: fresh %d hit %d", freshStatus, hitStatus)
	}
	if hits1 <= hits0 {
		t.Fatalf("the on-side request did not hit the response cache (hits %d -> %d, misses %d)", hits0, hits1, misses1)
	}
	for _, x := range []struct {
		name string
		raw  []byte
	}{{"fresh", freshRaw}, {"hit", hitRaw}} {
		head, _, _ := bytes.Cut(x.raw, []byte("\r\n\r\n"))
		if !bytes.Contains(head, []byte("Transfer-Encoding: chunked")) {
			t.Fatalf("%s: not chunked\n%s", x.name, x.raw)
		}
		if got := headerValue(head, "Content-Length"); got != "" {
			t.Fatalf("%s: Content-Length %q, want absent\n%s", x.name, got, x.raw)
		}
	}
	if !bytes.Equal(maskDate(freshRaw), maskDate(hitRaw)) {
		t.Fatalf("fresh and hit exchanges differ:\n%s\n--- vs ---\n%s", freshRaw, hitRaw)
	}
}
