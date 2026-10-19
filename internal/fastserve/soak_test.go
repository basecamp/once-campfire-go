package fastserve

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// soakBig is a response body large enough that a request leaving it unread
// exercises the post-handler drain and its close decision.
var soakBig = strings.Repeat("y", 512)

// openFDs counts this process's open file descriptors (Linux).
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("no /proc/self/fd")
		}
		t.Fatal(err)
	}
	return len(entries)
}

// waitFDs polls until the fd count is at or below limit, or times out. The
// server closes each connection asynchronously after the client's FIN, so a
// soak's fd count returns to the baseline only once those closes land.
func waitFDs(t *testing.T, limit int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if openFDs(t) <= limit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fd count stayed above %d for %v", limit, timeout)
}

// TestSoakConcurrentKeepAlive hammers the loop with concurrent keep-alive
// clients (mixed GET and POST, some with unread bodies), then checks that
// every descriptor the server opened is released once the clients leave:
// no per-request fd growth, no leaked conn goroutines. Run under -race.
func TestSoakConcurrentKeepAlive(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			w.Header().Set("Content-Length", strconv.Itoa(len(soakBig)))
			io.WriteString(w, soakBig)
			return
		}
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"ok":1}`)
	})
	addr := startFast(t, handler, func(s *Server) {
		// A long IdleTimeout keeps conns alive so the soak exercises the
		// keep-alive loop, not the accept loop.
		s.IdleTimeout = 60 * time.Second
	})
	baseline := openFDs(t)

	const clients = 24
	const rounds = 150
	var wg sync.WaitGroup
	for g := 0; g < clients; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(60 * time.Second))
			br := bufio.NewReader(c)
			for i := 0; i < rounds; i++ {
				var req string
				switch i % 3 {
				case 0:
					req = "GET / HTTP/1.1\r\nHost: h\r\n\r\n"
				case 1:
					req = "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 12\r\n\r\nhello world!"
				case 2:
					// An unread body: the drain path, every third round.
					req = "POST /big HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nzzzzz"
				}
				if _, err := c.Write([]byte(req)); err != nil {
					t.Errorf("client %d write %d: %v", g, i, err)
					return
				}
				if !strings.HasPrefix(abReadString(br), "HTTP/1.1 200") {
					t.Errorf("client %d round %d: bad status", g, i)
					return
				}
			}
		}()
	}
	// Mid-soak: the server must not be accumulating descriptors while
	// clients churn (accept + keep-alive + close cycles).
	time.Sleep(150 * time.Millisecond)
	if n := openFDs(t); n > baseline+clients+16 {
		t.Errorf("mid-soak fd count %d, baseline %d, %d clients", n, baseline, clients)
	}
	wg.Wait()
	// All client conns are closed; the server's side must follow.
	waitFDs(t, baseline+8, 10*time.Second)
}

// abReadString reads one response's status line (the soak asserts on status
// plus the implicit body framing below; the body is drained by framing).
func abReadString(br *bufio.Reader) string {
	line, err := br.ReadString('\n')
	if err != nil {
		return ""
	}
	contentLength := int64(-1)
	chunked := false
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return ""
		}
		if strings.Contains(strings.ToLower(l), "transfer-encoding:") && strings.Contains(strings.ToLower(l), "chunked") {
			chunked = true
		}
		if n, perr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(l, "Content-Length: ")), 10, 64); perr == nil {
			contentLength = n
		}
		if l == "\r\n" {
			break
		}
	}
	if chunked {
		for {
			sz, err := br.ReadString('\n')
			if err != nil {
				return ""
			}
			size, perr := strconv.ParseInt(strings.TrimSpace(sz), 16, 64)
			if perr != nil {
				return ""
			}
			if size == 0 {
				br.ReadString('\n')
				return line
			}
			if _, err := io.CopyN(io.Discard, br, size); err != nil {
				return ""
			}
			br.Discard(2)
		}
	}
	if contentLength >= 0 {
		if _, err := io.CopyN(io.Discard, br, contentLength); err != nil {
			return ""
		}
	}
	return line
}
