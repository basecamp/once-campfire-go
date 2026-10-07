package fastserve

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCanonicalHeaderKey pins the fast-path spellings against
// textproto.CanonicalMIMEHeaderKey across the case spellings clients send.
func TestCanonicalHeaderKey(t *testing.T) {
	names := []string{
		"host", "date", "range", "accept", "cookie", "expect", "origin",
		"pragma", "referer", "upgrade", "forwarded", "connection",
		"user-agent", "content-type", "x-csrf-token", "cache-control",
		"authorization", "if-none-match", "content-length", "accept-encoding",
		"x-forwarded-for", "x-request-start", "x-forwarded-host",
		"x-forwarded-port", "transfer-encoding", "if-modified-since",
		"x-forwarded-proto",
	}
	for _, name := range names {
		want := http.CanonicalHeaderKey(name)
		if got := canonicalHeaderKey([]byte(name)); got != want {
			t.Errorf("%q fast path => %q, want %q", name, got, want)
		}
		mixed := strings.ToUpper(name[:1]) + name[1:]
		if got := canonicalHeaderKey([]byte(mixed)); got != want {
			t.Errorf("%q (mixed %q) => %q, want %q", name, mixed, got, want)
		}
		if got := canonicalHeaderKey([]byte(strings.ToUpper(name))); got != want {
			t.Errorf("%q (upper) => %q, want %q", name, got, want)
		}
	}
	// The fallback must still match for names the fast path does not know.
	for _, name := range []string{"x-weird-Header", "ETag", "vary", "accept-language"} {
		want := http.CanonicalHeaderKey(name)
		if got := canonicalHeaderKey([]byte(name)); got != want {
			t.Errorf("%q fallback => %q, want %q", name, got, want)
		}
	}
}

// TestCanonicalHeaderKeyOnTheWire runs a request with mixed-case header names
// through a real loop and checks the handler sees the canonical keys.
func TestCanonicalHeaderKeyOnTheWire(t *testing.T) {
	got := make(chan http.Header, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		io.WriteString(w, "ok")
	})
	srv := New(handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(listener)
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	request := "GET / HTTP/1.1\r\nHost: h\r\nConnection: close\r\naCcept-ENcoding: gzip\r\nCoOkIe: a=b\r\nUSER-AGENT: probe\r\nX-CSRF-TOKEN: tok\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "200") {
		t.Fatalf("unexpected response: %q", body)
	}
	header := <-got
	if header.Get("Accept-Encoding") != "gzip" || header.Get("Cookie") != "a=b" ||
		header.Get("User-Agent") != "probe" || header.Get("X-Csrf-Token") != "tok" {
		t.Fatalf("canonical headers wrong: %v", header)
	}
}
