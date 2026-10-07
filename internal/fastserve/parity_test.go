package fastserve

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fmtErr renders an error for the differential corpus, stable across runs.
func fmtErr(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// The differential corpus: the exact same raw request bytes are sent to a
// stock net/http server and to this loop (same handler, same limits), and the
// full response bytes must match after masking the Date header, which differs
// between two exchanges by construction. Chunked responses are compared with
// their framing normalized (status, headers, decoded body): chunk boundaries
// are a function of the writer's buffering, not a semantic (net/http's own
// chunks differ by Write vs WriteString shape).

func startFast(t *testing.T, h http.Handler, tune func(*Server)) string {
	t.Helper()
	s := New(h)
	if tune != nil {
		tune(s)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Close()
		l.Close()
		<-done
	})
	return l.Addr().String()
}

func startOracle(t *testing.T, h http.Handler, tune func(*http.Server)) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	if tune != nil {
		tune(srv.Config)
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func rawExchange(t *testing.T, addr string, req []byte) []byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf
}

// rawOne writes req and reads exactly one response (status line, headers,
// body per Content-Length/framing, or until close), leaving the connection
// usable — the form needed for keep-alive comparisons.
func rawOne(t *testing.T, addr string, req []byte) []byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}
	br := bufio.NewReader(c)
	var out []byte
	// status line
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	out = append(out, line...)
	// headers
	chunked := false
	var contentLength int64 = -1
	for {
		line, err = br.ReadString('\n')
		if err != nil {
			t.Fatalf("header: %v", err)
		}
		out = append(out, line...)
		if line == "\r\n" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "transfer-encoding:") && strings.Contains(strings.ToLower(line), "chunked") {
			chunked = true
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(line[len("Content-Length:"):]), 10, 64); err == nil {
			contentLength = n
		}
	}
	if chunked {
		for {
			sizeLine, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("chunk size: %v", err)
			}
			out = append(out, sizeLine...)
			size, err := strconv.ParseInt(strings.TrimSpace(sizeLine), 16, 64)
			if err != nil {
				t.Fatalf("chunk size parse: %q", sizeLine)
			}
			if size == 0 {
				trailer, err := br.ReadString('\n')
				if err != nil {
					t.Fatalf("trailer: %v", err)
				}
				out = append(out, trailer...)
				break
			}
			body := make([]byte, size+2)
			if _, err := io.ReadFull(br, body); err != nil {
				t.Fatalf("chunk data: %v", err)
			}
			out = append(out, body...)
		}
		return out
	}
	if contentLength >= 0 {
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(br, body); err != nil {
			t.Fatalf("body: %v", err)
		}
		out = append(out, body...)
		return out
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("rest: %v", err)
	}
	out = append(out, rest...)
	return out
}

// corpusHandler covers the response shapes the application chain produces.
func corpusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hello":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Length", "11")
			w.Write([]byte("hello world"))
		case "/autoct":
			io.WriteString(w, "<h1>hello</h1>") // sniffed Content-Type, auto CL
		case "/empty":
			w.WriteHeader(http.StatusOK) // no writes: auto Content-Length: 0
		case "/n204":
			w.WriteHeader(http.StatusNoContent)
		case "/n304":
			w.Header().Set("ETag", `"x"`)
			w.WriteHeader(http.StatusNotModified)
		case "/head":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("partial"))
		case "/big":
			io.WriteString(w, strings.Repeat("z", 3000)) // chunked on both
		case "/stream":
			w.Write([]byte("a"))
			http.NewResponseController(w).Flush()
			w.Write([]byte("b"))
		case "/json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			io.WriteString(w, `{"ok":1}`)
		case "/redirect":
			http.Redirect(w, r, "/hello", http.StatusFound)
		case "/gone":
			http.NotFound(w, r)
		case "/cookies":
			http.SetCookie(w, &http.Cookie{Name: "a", Value: "1"})
			http.SetCookie(w, &http.Cookie{Name: "b", Value: "2"})
		case "/413":
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
		case "/readall":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body)
		case "/echo":
			io.Copy(w, r.Body)
		case "/notread":
			w.Write([]byte("ok")) // leaves the request body unread (drain path)
		case "/te":
			// A handler-set Transfer-Encoding must not also get an
			// automatic Content-Length (net/http's !hasTE gate).
			w.Header().Set("Transfer-Encoding", "chunked")
			io.WriteString(w, "te-body")
		case "/limit":
			// An http.MaxBytesReader overflow mid-read marks the response
			// requestTooLarge: Connection: close and no reuse.
			r.Body = http.MaxBytesReader(w, r.Body, 8)
			n, err := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "read "+strconv.Itoa(len(n))+" err "+fmtErr(err))
		default:
			http.Error(w, "no such route", http.StatusNotFound)
		}
	})
}

// parityCase is one corpus entry: the raw request, sent verbatim to both
// servers. chunked marks responses whose bodies are chunk-framed and compared
// decoded.
type parityCase struct {
	name    string
	request string
}

var parityCorpus = []parityCase{
	{"basic GET 1.1", "GET /hello HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"GET with folded header", "GET /hello HTTP/1.1\r\nHost: h\r\nX-A: 1\r\nx-a: 2\r\nConnection: close\r\n\r\n"},
	{"GET pragma fold", "GET /hello HTTP/1.1\r\nHost: h\r\nPragma: no-cache\r\nConnection: close\r\n\r\n"},
	{"auto content-type", "GET /autoct HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"empty 200", "GET /empty HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"204", "GET /n204 HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"304", "GET /n304 HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"HEAD with declared CL", "HEAD /head HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"chunked response big", "GET /big HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"chunked response stream", "GET /stream HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"json", "GET /json HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"302 redirect", "GET /redirect HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"404", "GET /gone HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"two Set-Cookie", "GET /cookies HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"413 via http.Error", "GET /413 HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"POST fixed body", "POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello"},
	{"POST empty body", "POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"},
	{"POST chunked body", "POST /readall HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n5\r\nhello\r\n0\r\n\r\n"},
	{"POST body unread", "POST /notread HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello"},
	{"POST body unread big", "POST /notread HTTP/1.1\r\nHost: h\r\nContent-Length: 300000\r\nConnection: close\r\n\r\n" + strings.Repeat("x", 300000)},
	{"response TE no auto CL", "GET /te HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"maxbytes overflow mid-read", "POST /limit HTTP/1.1\r\nHost: h\r\nContent-Length: 40\r\n\r\n" + strings.Repeat("x", 40)},
	{"echo chunked", "POST /echo HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n3\r\nabc\r\n2\r\nde\r\n0\r\n\r\n"},
	{"100-continue", "POST /readall HTTP/1.1\r\nHost: h\r\nExpect: 100-continue\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello"},
	{"100-continue body unread", "POST /notread HTTP/1.1\r\nHost: h\r\nExpect: 100-continue\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello"},
	{"non-100 expect 417", "GET /hello HTTP/1.1\r\nHost: h\r\nExpect: gzip\r\nConnection: close\r\n\r\n"},
	{"OPTIONS asterisk", "OPTIONS * HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"HTTP/1.0 close", "GET /hello HTTP/1.0\r\n\r\n"},
	{"HTTP/1.0 keep-alive", "GET /hello HTTP/1.0\r\nConnection: keep-alive\r\n\r\n"},
	{"HTTP/1.0 keep-alive empty", "GET /empty HTTP/1.0\r\nConnection: keep-alive\r\n\r\n"},
	{"Connection close echoed", "GET /hello HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	// The wire-level rejection corpus: any head simdhttp rejects that
	// net/http also rejects must produce the same 400/431 bytes.
	{"garbage head", "GARBAGE\r\n\r\n"},
	{"missing Host", "GET /hello HTTP/1.1\r\n\r\n"},
	{"header without colon", "GET /hello HTTP/1.1\r\nHost: h\r\nBadHeader\r\n\r\n"},
	{"two different content-lengths", "POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\nContent-Length: 6\r\n\r\nhello!"},
	{"absolute-form target", "GET http://example.com/hello HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"},
	{"control byte in target", "GET /he\x01llo HTTP/1.1\r\nHost: h\r\n\r\n"},
	{"bad percent escape", "GET /he%zzllo HTTP/1.1\r\nHost: h\r\n\r\n"},
	{"control byte in header value", "GET /hello HTTP/1.1\r\nHost: h\r\nX-Bad: a\x01b\r\n\r\n"},
	{"bad content-length value", "POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 12x\r\n\r\n"},
}

func TestParityCorpus(t *testing.T) {
	handler := corpusHandler()
	oracle := startOracle(t, handler, nil)
	fast := startFast(t, handler, nil)
	for _, c := range parityCorpus {
		c := c
		t.Run(c.name, func(t *testing.T) {
			exchange := rawExchange
			if c.name == "HTTP/1.0 keep-alive" || c.name == "HTTP/1.0 keep-alive empty" {
				exchange = rawOne // keep-alive conns never EOF on their own
			}
			if c.name == "POST body unread big" {
				exchange = rawOne // net/http closes with a reset; the response precedes it
			}
			want := normalize(exchange(t, oracle, []byte(c.request)))
			got := normalize(exchange(t, fast, []byte(c.request)))
			if !bytes.Equal(want, got) {
				t.Fatalf("response mismatch\n--- net/http ---\n%s\n--- fastserve ---\n%s", want, got)
			}
		})
	}
}

// TestParityDeltas pins the documented differences from net/http, inherited
// from the simdhttp head parser (package comment, plans/engine-41.md): every
// case here is deliberately stricter on this loop, and each verdict is
// asserted so a future simdhttp bump cannot silently change it.
func TestParityDeltas(t *testing.T) {
	fast := startFast(t, corpusHandler(), nil)
	cases := []struct {
		name    string
		request string
		want    string // the exact status line this loop must answer
	}{
		{"bare LF line endings", "GET /hello HTTP/1.1\nHost: h\n\n", "HTTP/1.1 400 Bad Request"},
		{"unsupported version is 400 here, 505 in net/http", "GET /hello HTTP/9.9\r\nHost: h\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"space in header name is 400 here, text differs", "GET /hello HTTP/1.1\r\nHost: h\r\nBad Name: x\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"two transfer-encodings is 400 here, 501 in net/http", "POST /readall HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"unsupported transfer encoding is 400 here, 501 in net/http", "POST /readall HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: gzip\r\n\r\n", "HTTP/1.1 400 Bad Request"},
		{"transfer-encoding plus content-length is 400 here", "POST /readall HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nContent-Length: 5\r\n\r\nhello", "HTTP/1.1 400 Bad Request"},
		{"duplicate equal content-lengths is 400 here", "POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\nContent-Length: 5\r\n\r\nhello", "HTTP/1.1 400 Bad Request"},
		{"over-long request line is 431 here", "GET /" + strings.Repeat("a", 9<<10) + " HTTP/1.1\r\nHost: h\r\n\r\n", "HTTP/1.1 431 Request Header Fields Too Large"},
		{"more than 100 headers is 431 here", "GET /hello HTTP/1.1\r\nHost: h\r\n" + strings.Repeat("X-F: v\r\n", 150) + "\r\n", "HTTP/1.1 431 Request Header Fields Too Large"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := rawExchange(t, fast, []byte(c.request))
			if !bytes.HasPrefix(got, []byte(c.want)) {
				t.Fatalf("verdict %q, want prefix %q", got, c.want)
			}
			if !bytes.Contains(got, []byte("Connection: close")) {
				t.Fatalf("rejections must close: %q", got)
			}
		})
	}
}

// TestParitySmallHeaderLimit pins the 431 path: both servers are capped at a
// 4 KiB head and must answer the same bytes for an over-long head.
func TestParitySmallHeaderLimit(t *testing.T) {
	handler := corpusHandler()
	big := "GET /hello HTTP/1.1\r\nHost: h\r\nX-Big: " + strings.Repeat("a", 8<<10) + "\r\nConnection: close\r\n\r\n"
	oracle := startOracle(t, handler, func(s *http.Server) { s.MaxHeaderBytes = 4096 })
	fast := startFast(t, handler, func(s *Server) { s.MaxHeaderBytes = 4096 })
	want := normalize(rawExchange(t, oracle, []byte(big)))
	got := normalize(rawExchange(t, fast, []byte(big)))
	if !bytes.Equal(want, got) {
		t.Fatalf("431 mismatch\n--- net/http ---\n%s\n--- fastserve ---\n%s", want, got)
	}
	if !bytes.Contains(want, []byte("431 Request Header Fields Too Large")) {
		t.Fatalf("expected 431, got %q", want)
	}
}

// TestParityKeepAliveReuse runs a sequence of requests over one connection
// and compares the exact byte stream to net/http's: keep-alive reuse, the
// post-POST leading CRLF tolerance, and pipelined requests are all covered.
func TestParityKeepAliveReuse(t *testing.T) {
	handler := corpusHandler()
	oracle := startOracle(t, handler, nil)
	fast := startFast(t, handler, nil)
	seq := strings.Join([]string{
		"POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\n\r\nabc",
		"POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\n\r\ndef\r\n\r\n", // + leading CRLF tolerance
		"GET /hello HTTP/1.1\r\nHost: h\r\n\r\n",
		"GET /notread HTTP/1.1\r\nHost: h\r\nContent-Length: 2\r\n\r\nzz", // drained mid-stream
		"GET /hello HTTP/1.0\r\nConnection: keep-alive\r\n\r\n",
		"GET /empty HTTP/1.0\r\n\r\n", // 1.0 close ends the connection
	}, "")
	want := rawExchange(t, oracle, []byte(seq))
	got := rawExchange(t, fast, []byte(seq))
	if !bytes.Equal(want, got) {
		t.Fatalf("keep-alive stream mismatch\n--- net/http ---\n%s\n--- fastserve ---\n%s", want, got)
	}
}

// TestParityPipelined executes two requests sent back-to-back in one write.
func TestParityPipelined(t *testing.T) {
	handler := corpusHandler()
	oracle := startOracle(t, handler, nil)
	fast := startFast(t, handler, nil)
	raw := "GET /hello HTTP/1.1\r\nHost: h\r\n\r\nGET /json HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"
	want := normalize(rawExchange(t, oracle, []byte(raw)))
	got := normalize(rawExchange(t, fast, []byte(raw)))
	if !bytes.Equal(want, got) {
		t.Fatalf("pipelined mismatch\n--- net/http ---\n%s\n--- fastserve ---\n%s", want, got)
	}
}

// TestParityUpgradeNotHijacked sends an Upgrade-headed request the handler
// does not hijack; both loops must answer identically.
func TestParityUpgradeNotHijacked(t *testing.T) {
	handler := corpusHandler()
	oracle := startOracle(t, handler, nil)
	fast := startFast(t, handler, nil)
	raw := "GET /hello HTTP/1.1\r\nHost: h\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\nConnection: close\r\n\r\n"
	want := normalize(rawExchange(t, oracle, []byte(raw)))
	got := normalize(rawExchange(t, fast, []byte(raw)))
	if !bytes.Equal(want, got) {
		t.Fatalf("upgrade-not-hijacked mismatch\n--- net/http ---\n%s\n--- fastserve ---\n%s", want, got)
	}
}

// normalize masks Date (differs between two exchanges by construction) and
// decodes chunked framing so chunk boundaries do not fail the comparison.
func normalize(b []byte) []byte {
	idx := bytes.Index(b, []byte("\r\n\r\n"))
	if idx < 0 {
		return b
	}
	head, body := b[:idx+4], b[idx+4:]
	head = removeDate(head)
	var out []byte
	if bytes.Contains(head, []byte("Transfer-Encoding: chunked")) {
		body = dechunk(body)
		out = make([]byte, 0, len(head)+len(body))
		out = append(out, head...)
		out = append(out, body...)
		return out
	}
	out = make([]byte, 0, len(head)+len(body))
	out = append(out, head...)
	out = append(out, body...)
	return out
}

func removeDate(head []byte) []byte {
	line := []byte("\r\nDate: ")
	if i := bytes.Index(head, line); i >= 0 {
		end := bytes.Index(head[i+2:], []byte("\r\n"))
		if end >= 0 {
			head = append(head[:i], head[i+2+end:]...)
		}
	}
	return head
}

// dechunk decodes HTTP/1.1 chunked framing, leaving the trailers (none are
// produced by the corpus handlers).
func dechunk(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			return out
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(b[:nl])), 16, 64)
		if err != nil || size < 0 {
			return out
		}
		b = b[nl+1:]
		if size == 0 {
			return out
		}
		out = append(out, b[:size]...)
		b = b[size+2:] // data + CRLF
	}
	return out
}
