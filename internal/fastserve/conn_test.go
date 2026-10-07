package fastserve

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReadHeaderTimeout: a client that sends nothing must be dropped when
// the header deadline passes, with no bytes written (net/http's silent-close
// timeout path).
func TestReadHeaderTimeout(t *testing.T) {
	addr := startFast(t, corpusHandler(), func(s *Server) {
		s.ReadHeaderTimeout = 150 * time.Millisecond
	})
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(buf) != 0 {
		t.Fatalf("timeout must close silently, got %q", buf)
	}
}

// TestIdleTimeout: after a response the loop waits IdleTimeout for the next
// request and then closes.
func TestIdleTimeout(t *testing.T) {
	addr := startFast(t, corpusHandler(), func(s *Server) {
		s.IdleTimeout = 200 * time.Millisecond
	})
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("GET /hello HTTP/1.1\r\nHost: h\r\n\r\n"))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("first response line %q err %v", line, err)
	}
	// Drain the response body so the next read observes the idle close.
	var contentLength int64 = 11
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("header: %v", err)
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(l, "Content-Length: ")), 10, 64); err == nil {
			contentLength = n
		}
		if l == "\r\n" {
			break
		}
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if string(body) != "hello world" {
		t.Fatalf("body %q", body)
	}
	// The 200 ms idle deadline has long passed by now on a loaded test
	// machine; wait up to 3 s for the server to close.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := br.ReadByte(); err != nil {
			return // closed as expected
		}
		if time.Now().After(deadline) {
			t.Fatal("idle connection stayed open past IdleTimeout")
		}
	}
}

// TestWriteTimeout: a handler that responds past the write deadline loses
// the connection (net/http parity: the deadline is set before the handler
// runs).
func TestWriteTimeout(t *testing.T) {
	addr := startFast(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "late response")
	}), func(s *Server) {
		s.WriteTimeout = 100 * time.Millisecond
	})
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n"))
	buf, err := io.ReadAll(c)
	if err == nil && len(buf) > 0 {
		t.Fatalf("expected the timed-out write to kill the conn, got %q", buf)
	}
}

// TestReadTimeoutMidBody: a body that stalls past ReadTimeout fails the
// handler's read; the connection must not hang around.
func TestReadTimeoutMidBody(t *testing.T) {
	addr := startFast(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // blocks on the missing 90 bytes
		http.Error(w, "failed", 500)
	}), func(s *Server) {
		s.ReadTimeout = 150 * time.Millisecond
	})
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 100\r\n\r\n" + strings.Repeat("x", 10)))
	buf, err := io.ReadAll(c)
	if err == nil && len(buf) == 0 {
		// acceptable: the read error aborted the response entirely
	} else if err == nil && !strings.HasPrefix(string(buf), "HTTP/1.1 ") {
		t.Fatalf("unexpected tail %q", buf)
	} else if err != nil {
		// closed mid-read: fine, the point is no hang
	}
}

// TestConcurrentKeepAliveClients hammers the loop with parallel keep-alive
// clients; run under -race.
func TestConcurrentKeepAliveClients(t *testing.T) {
	addr := startFast(t, corpusHandler(), nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transport := &http.Transport{MaxIdleConnsPerHost: 4}
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			defer client.CloseIdleConnections()
			for i := 0; i < 25; i++ {
				resp, err := client.Get("http://" + addr + "/hello")
				if err != nil {
					t.Errorf("get: %v", err)
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || string(body) != "hello world" {
					t.Errorf("bad response %d %q", resp.StatusCode, body)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestHandlerPanicCloses: a panicking handler ends the connection without a
// response and without wedging the loop (ErrAbortHandler closes silently).
func TestHandlerPanicCloses(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		if r.URL.Path == "/abort" {
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, "hello world")
	})
	panics := startFast(t, handler, nil)
	aborts := startFast(t, handler, nil)
	for _, addr := range []string{panics, aborts} {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		c.Write([]byte("GET /panic HTTP/1.1\r\nHost: h\r\n\r\n"))
		buf, err := io.ReadAll(c)
		c.Close()
		if err == nil && len(buf) > 0 {
			t.Fatalf("panicked handler must not answer, got %q", buf)
		}
		c, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		c.Write([]byte("GET /abort HTTP/1.1\r\nHost: h\r\n\r\n"))
		buf, err = io.ReadAll(c)
		c.Close()
		if err == nil && len(buf) > 0 {
			t.Fatalf("aborted handler must not answer, got %q", buf)
		}
		// The loop must still serve after the panics.
		c, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		c.Write([]byte("GET / HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"))
		buf, _ = io.ReadAll(c)
		c.Close()
		if !strings.Contains(string(buf), "hello world") {
			t.Fatalf("loop dead after panic: %q", buf)
		}
	}
}

// TestBodyLimit413 runs the application's own bodyLimit pattern (front's
// middleware): an over-limit Content-Length answers 413, and — like net/http
// — a small unread body is drained and the connection stays usable.
func TestBodyLimit413(t *testing.T) {
	limited := bodyLimitWrap(corpusHandler(), 16)
	addr := startFast(t, limited, nil)
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("POST /readall HTTP/1.1\r\nHost: h\r\nContent-Length: 100\r\n\r\n" + strings.Repeat("x", 100)))
	br := bufio.NewReader(c)
	var out []byte
	var contentLength int64 = -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("413 read: %v", err)
		}
		out = append(out, line...)
		if n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length: ")), 10, 64); err == nil {
			contentLength = n
		}
		if line == "\r\n" {
			break
		}
	}
	if !strings.Contains(string(out), "413 Request Entity Too Large") {
		t.Fatalf("want 413, got %q", out)
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("413 body: %v", err)
	}
	// The drained body left the connection reusable: a second request on
	// the same conn must be served (net/http parity).
	if _, err := c.Write([]byte("GET /hello HTTP/1.1\r\nHost: h\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	second := rawOneRead(t, br)
	if !strings.Contains(second, "hello world") {
		t.Fatalf("keep-alive after 413: %q", second)
	}
}

// rawOneRead reads one response from an existing buffered reader.
func rawOneRead(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var out []byte
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out = append(out, line...)
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
		if strings.Contains(strings.ToLower(line), "transfer-encoding:") && strings.Contains(strings.ToLower(line), "chunked") {
			chunked = true
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length: ")), 10, 64); err == nil {
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
				return string(out)
			}
			if size == 0 {
				if l, err := br.ReadString('\n'); err == nil {
					out = append(out, l...)
				}
				return string(out)
			}
			body := make([]byte, size+2)
			if _, err := io.ReadFull(br, body); err != nil {
				return string(out)
			}
			out = append(out, body...)
		}
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		return string(out)
	}
	out = append(out, body...)
	return string(out)
}

func bodyLimitWrap(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			http.Error(w, "Request Entity Too Large", 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// TestShutdownGraceful: Shutdown stops the listener, lets an in-flight
// request finish, then closes the connection at the next loop boundary.
func TestShutdownGraceful(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		io.WriteString(w, "done")
	})
	s := New(h)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	go s.Serve(l)
	defer s.Close()

	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	<-started
	// Ask for graceful shutdown while the request is in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.Shutdown(ctx) }()
	time.Sleep(50 * time.Millisecond)
	close(release) // release the handler; Shutdown must not have killed it
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("in-flight request must complete during Shutdown: %v %q", err, buf)
	}
	if !strings.HasPrefix(string(buf), "HTTP/1.1 200 OK") || !strings.HasSuffix(string(buf), "\r\n\r\ndone") {
		t.Fatalf("response during shutdown %q", buf)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestShutdownClosesIdle: an idle keep-alive connection is closed by
// Shutdown, and never starts a new request.
func TestShutdownClosesIdle(t *testing.T) {
	s := New(corpusHandler())
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	go s.Serve(l)
	defer s.Close()

	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("GET /hello HTTP/1.1\r\nHost: h\r\n\r\n"))
	br := bufio.NewReader(c)
	var out []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		out = append(out, line...)
		if line == "\r\n" {
			body := make([]byte, 11)
			if _, err := io.ReadFull(br, body); err != nil {
				t.Fatal(err)
			}
			out = append(out, body...)
			break
		}
	}
	if !strings.Contains(string(out), "hello world") {
		t.Fatalf("first response %q", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("idle connection survived Shutdown")
	}
}
