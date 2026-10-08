package fastserve

// The in-app A/B (ENGINE-53): the native bench/application request set,
// measured at the loop level on the same handler — recorded-piece room page
// (identity, ~374 KB emitted as shell parts + message-list payload, the
// precomposed emission shape), sidebar fragment, search results, and a form
// POST answering 302. Each route runs one keep-alive raw connection against
// net/http and against this loop; interleaved runs compare medians (the
// official number sits behind benchstat on alternating -count runs).
//
// The handler is deliberately identical for both loops and writes through
// the ResponseWriter contract the web layer uses, so the difference is the
// conn loop: simdhttp head parsing + vectored writes on this side, textproto
// + two bufio layers on net/http's.

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// abBodies are deterministic, varied HTML fragments sized like the app's
// renders: the room page is 4 parts (3 shell segments + the message list),
// sidebar and search are single fragments.
var (
	abRoomParts = makeRoomParts()
	abSidebar   = abMarkup(12<<10, "sidebar")
	abSearch    = abMarkup(4<<10, "search")
	abRoomSize  int
)

func init() {
	for _, p := range abRoomParts {
		abRoomSize += len(p)
	}
}

func abMarkup(size int, seed string) string {
	block := "<div class=\"c\"><span>" + seed + "</span><p>fragment body text for a representative response</p></div>\n"
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(block)
	}
	return b.String()
}

func makeRoomParts() []string {
	return []string{
		abMarkup(6<<10, "shell-a"),
		abMarkup(6<<10, "shell-b"),
		abMarkup(4<<10, "shell-c"),
		abMarkup(352<<10, "message payload"),
	}
}

func abHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/room":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(abRoomSize))
			for _, p := range abRoomParts {
				io.WriteString(w, p)
			}
		case "/sidebar":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, abSidebar)
		case "/search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, abSearch)
		case "/post":
			io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, "/room", http.StatusFound)
		}
	})
}

const (
	abRoomRequest = "GET /room HTTP/1.1\r\nHost: h\r\n\r\n"
	abSidebarReq  = "GET /sidebar HTTP/1.1\r\nHost: h\r\n\r\n"
	abSearchReq   = "GET /search HTTP/1.1\r\nHost: h\r\n\r\n"
	abPostReq     = "POST /post HTTP/1.1\r\nHost: h\r\nContent-Length: 48\r\nContent-Type: application/x-www-form-urlencoded\r\n\r\nmessage_body=hello&thread_id=42&timestamp=1767225845"
)

// abServe sends request over one keep-alive connection, reading each
// response by its Content-Length (every A/B route carries one, including the
// 302), warms the connection once, then measures b.N exchanges.
func abServe(b *testing.B, addr, request string) {
	b.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(120 * time.Second))
	br := bufio.NewReaderSize(c, 1<<16)
	req := []byte(request)
	first := true
	for i := 0; i < b.N+1; i++ {
		if _, err := c.Write(req); err != nil {
			b.Fatal(err)
		}
		if err := abReadResponse(br); err != nil {
			b.Fatal(err)
		}
		if first {
			first = false
			b.ResetTimer()
		}
	}
}

// abReadResponse reads one response: status line, headers, then the body per
// Content-Length (or to close, when no length is declared).
func abReadResponse(br *bufio.Reader) error {
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "HTTP/1.1 ") {
		if err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	}
	contentLength := int64(-1)
	for {
		line, err = br.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" {
			break
		}
		if n, perr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length: ")), 10, 64); perr == nil {
			contentLength = n
		}
	}
	if contentLength >= 0 {
		_, err = io.CopyN(io.Discard, br, contentLength)
		return err
	}
	// Chunked framing (no declared length): walk the chunk sizes.
	for {
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		size, perr := strconv.ParseInt(strings.TrimSpace(sizeLine), 16, 64)
		if perr != nil || size < 0 {
			return io.ErrUnexpectedEOF
		}
		if size == 0 {
			_, err = br.ReadString('\n') // trailing CRLF
			return err
		}
		if _, err := io.CopyN(io.Discard, br, size); err != nil {
			return err
		}
		if _, err := br.Discard(2); err != nil { // chunk CRLF
			return err
		}
	}
}

func BenchmarkABNetHTTP(b *testing.B) {
	srv := httptest.NewServer(abHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	for _, r := range []struct{ name, req string }{
		{"room", abRoomRequest}, {"sidebar", abSidebarReq},
		{"search", abSearchReq}, {"post", abPostReq},
	} {
		b.Run(r.name, func(b *testing.B) { abServe(b, addr, r.req) })
	}
}

func BenchmarkABFast(b *testing.B) {
	s := New(abHandler())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(l)
	defer s.Close()
	addr := l.Addr().String()
	for _, r := range []struct{ name, req string }{
		{"room", abRoomRequest}, {"sidebar", abSidebarReq},
		{"search", abSearchReq}, {"post", abPostReq},
	} {
		b.Run(r.name, func(b *testing.B) { abServe(b, addr, r.req) })
	}
}
