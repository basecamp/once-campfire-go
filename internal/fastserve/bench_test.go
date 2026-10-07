package fastserve

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

// The loop-vs-net/http serving benchmark, the ENGINE-41 gate shape: one
// keep-alive raw connection, sequential GETs, both loops on the same
// handler. Head parsing is simdhttp on one side and net/http's textproto
// reader on the other; responses differ in write shape (this loop emits one
// vectored segment per response, net/http writes through two bufio layers).
func benchServe(b *testing.B, addr string, request []byte) {
	b.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(120 * time.Second))
	br := bufio.NewReaderSize(c, 1<<16)
	readResponse := func() {
		line, err := br.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "HTTP/1.1 200") {
			b.Fatalf("status %q err %v", line, err)
		}
		var contentLength = -1
		for {
			line, err = br.ReadString('\n')
			if err != nil {
				b.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length: "))); err == nil {
				contentLength = n
			}
		}
		if _, err := io.CopyN(io.Discard, br, int64(contentLength)); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := c.Write(request); err != nil {
		b.Fatal(err)
	}
	readResponse() // first exchange warms the connection path
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(request); err != nil {
			b.Fatal(err)
		}
		readResponse()
	}
}

func benchHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := "hello world, this is a fixed-size response body padded out to a representative page size for the connected benchmark." + strings.Repeat("x", 418-len("hello world, this is a fixed-size response body padded out to a representative page size for the connected benchmark."))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "418")
		io.WriteString(w, body)
	})
}

const benchRequest = "GET / HTTP/1.1\r\nHost: h\r\n\r\n"

func BenchmarkServeNetHTTPSingleConn(b *testing.B) {
	srv := httptest.NewServer(benchHandler())
	defer srv.Close()
	benchServe(b, srv.Listener.Addr().String(), []byte(benchRequest))
}

func BenchmarkServeFastSingleConn(b *testing.B) {
	s := New(benchHandler())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(l)
	defer s.Close()
	benchServe(b, l.Addr().String(), []byte(benchRequest))
}
