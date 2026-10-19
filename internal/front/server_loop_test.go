package front

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestServerLoopFlagDiff runs the real front.Serve composition — bodyLimit,
// Deflate, the cache and public chains — once with the owned loop on and once
// off (the current net/http listener), and compares raw wire bytes for the
// response shapes the application produces. Date is masked; a second, keep-
// alive request proves the loop reuses connections the same way.
func TestServerLoopFlagDiff(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/compress":
			io.WriteString(w, "<h1>"+strings.Repeat("hello ", 200)+"</h1>")
		case "/small":
			io.WriteString(w, "ok")
		case "/notfound":
			http.NotFound(w, r)
		case "/empty":
			w.WriteHeader(200)
		default:
			io.WriteString(w, "root:"+r.URL.Path)
		}
	})
	run := func(loop bool) string {
		config := FromLookup(func(string) (string, bool) { return "", false })
		config.TargetBind = "127.0.0.1"
		config.ServerLoop = loop
		config.LogRequests = false
		// The port chosen by freePort can be claimed by a concurrent
		// test binary's :0 bind before Serve binds it explicitly, which
		// would make waitPort time out; retry with a fresh port.
		for attempt := 0; ; attempt++ {
			port := freePort(t)
			config.TargetPort = port
			config.HTTPPort = port + 1 // distinct ports so the target listener exists
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- Serve(ctx, config, app) }()
			if waitPortBool(port) {
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("front.Serve did not stop")
					}
				})
				return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			}
			cancel()
			select {
			case err := <-done:
				if attempt == 2 {
					t.Fatalf("front.Serve failed: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("front.Serve did not stop after a failed listen")
			}
		}
	}
	off := run(false)
	on := run(true)

	// The request corpus, byte-compared after masking Date. Each request
	// closes so the exchange has a definite end (the loop-off server keeps
	// connections alive the same way the loop-on one does; the keep-alive
	// path itself is covered by the second stage below).
	cases := []struct {
		name    string
		request string
	}{
		{"plain small", "GET /small HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
		{"plain compressible", "GET /compress HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
		{"gzip", "GET /compress HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n"},
		{"gzip small", "GET /small HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n"},
		{"gzip identity q0", "GET /compress HTTP/1.1\r\nHost: h\r\nAccept-Encoding: identity;q=0, gzip;q=0\r\nConnection: close\r\n\r\n"},
		{"encoded identity", "GET /small HTTP/1.1\r\nHost: h\r\nAccept-Encoding: identity\r\nConnection: close\r\n\r\n"},
		{"not found", "GET /notfound HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
		{"empty 200", "GET /empty HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
		{"head", "HEAD /small HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
		{"post form", "POST /small HTTP/1.1\r\nHost: h\r\nContent-Length: 11\r\nContent-Type: application/x-www-form-urlencoded\r\nConnection: close\r\n\r\na=b&c=d%20e"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			want := maskDate(exchange(t, off, []byte(c.request)))
			got := maskDate(exchange(t, on, []byte(c.request)))
			if !bytes.Equal(want, got) {
				t.Fatalf("wire mismatch (loop off vs on)\n--- off ---\n%s\n--- on ---\n%s", want, got)
			}
		})
	}

	// Keep-alive: three requests in sequence on one connection must produce
	// the same byte stream on both loops.
	seq := "GET /small HTTP/1.1\r\nHost: h\r\n\r\n" +
		"GET /compress HTTP/1.1\r\nHost: h\r\nAccept-Encoding: gzip\r\n\r\n" +
		"GET /empty HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"
	if want, got := maskDate(exchange(t, off, []byte(seq))), maskDate(exchange(t, on, []byte(seq))); !bytes.Equal(want, got) {
		t.Fatalf("keep-alive stream mismatch\n--- off ---\n%s\n--- on ---\n%s", want, got)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitPort(t *testing.T, port int) {
	t.Helper()
	if !waitPortBool(port) {
		t.Fatalf("listener on %d never came up", port)
	}
}

// waitPortBool reports whether a listener accepted a connection on port
// within the deadline, without failing the test.
func waitPortBool(port int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// exchange sends raw bytes and returns everything until the connection
// closes, with a generous deadline.
func exchange(t *testing.T, addr string, req []byte) []byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf
}

// maskDate drops the Date header line, which differs between two exchanges
// of the same request by construction.
func maskDate(b []byte) []byte {
	var out []byte
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Date: ") {
			continue
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	return out
}

// TestServerLoopFlagEnv pins the CAMPFIRE_SERVER_LOOP parsing: on/off/1/0
// and the default (on, with "off" the documented rollback).
func TestServerLoopFlagEnv(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		}
	}
	cases := []struct {
		value string
		want  bool
	}{
		{"", true}, // unset: the loop is the default
		{"on", true},
		{"ON", true},
		{"true", true},
		{"1", true},
		{"off", false},
		{"false", false},
		{"0", false},
		{"bogus", true}, // unrecognized keeps the default
	}
	for _, c := range cases {
		values := map[string]string{}
		if c.value != "" {
			values["CAMPFIRE_SERVER_LOOP"] = c.value
		}
		if got := FromLookup(lookup(values)).ServerLoop; got != c.want {
			t.Errorf("CAMPFIRE_SERVER_LOOP=%q: loop=%v, want %v", c.value, got, c.want)
		}
	}
}
