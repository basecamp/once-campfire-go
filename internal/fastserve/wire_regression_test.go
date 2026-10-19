package fastserve

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A timed-out socket can become writable again after clearing its deadline.
// Recoverability of the socket must not make a partial HTTP response reusable.
type prefixFaultConn struct {
	net.Conn
	in                        *strings.Reader
	out                       bytes.Buffer
	failAt                    int
	writes                    int
	failed, recovered, closed bool
}

func (c *prefixFaultConn) Read(p []byte) (int, error) { return c.in.Read(p) }
func (c *prefixFaultConn) Write(p []byte) (int, error) {
	c.writes++
	if c.failed && !c.recovered {
		return 0, os.ErrDeadlineExceeded
	}
	if c.writes == c.failAt {
		c.failed = true
		n := min(12, len(p)/2)
		c.out.Write(p[:n])
		return n, os.ErrDeadlineExceeded
	}
	return c.out.Write(p)
}
func (c *prefixFaultConn) Close() error                    { c.closed = true; return nil }
func (c *prefixFaultConn) RemoteAddr() net.Addr            { return nil }
func (c *prefixFaultConn) SetReadDeadline(time.Time) error { return nil }
func (c *prefixFaultConn) SetWriteDeadline(t time.Time) error {
	if t.IsZero() {
		c.recovered = true
	}
	return nil
}

func TestWriteFailureStopsPipeline(t *testing.T) {
	for _, mode := range []string{"precomposed", "empty", "terminal", "flush", "interim", "body"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var emissionErr error
			s := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 1 {
					io.WriteString(w, "SECOND")
					return
				}
				switch mode {
				case "precomposed":
					emissionErr = w.(*response).WritePrecomposed(200, []byte("Content-Length: 100\r\n"), [][]byte{bytes.Repeat([]byte("x"), 100)})
				case "terminal":
					io.WriteString(w, "x")
					w.(http.Flusher).Flush()
				case "flush":
					emissionErr = http.NewResponseController(w).Flush()
				case "interim":
					w.WriteHeader(103)
				case "body":
					_, emissionErr = w.Write(bytes.Repeat([]byte("x"), 3000))
				}
			}))
			s.WriteTimeout = time.Second
			f := &prefixFaultConn{in: strings.NewReader("GET / HTTP/1.1\r\nHost: x\r\n\r\nGET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"), failAt: 1}
			if mode == "terminal" {
				f.failAt = 5
			} // head, chunk size, data, CRLF, zero chunk
			c := newConn(s, f)
			s.wg.Add(1)
			c.serve()
			if !f.failed {
				t.Fatal("fault was not reached")
			}
			if !f.closed || calls != 1 || bytes.Contains(f.out.Bytes(), []byte("SECOND")) || bytes.Count(f.out.Bytes(), []byte("HTTP/1.1")) != 1 {
				t.Errorf("closed=%v handlers=%d wire=%q", f.closed, calls, f.out.Bytes())
			}
			if (mode == "precomposed" || mode == "flush" || mode == "body") && emissionErr == nil {
				t.Error("emission did not return failure")
			}
		})
	}
}

func TestEmptyFlushHeaders(t *testing.T) {
	for _, proto := range []string{"HTTP/1.0", "HTTP/1.1"} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, status := range []int{200, 204, 304} {
				t.Run(fmt.Sprintf("%s/%s/%d", proto, method, status), func(t *testing.T) {
					var wires [2][]byte
					for i := range wires {
						release := make(chan struct{})
						h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							w.WriteHeader(status)
							w.(http.Flusher).Flush()
							<-release
							io.WriteString(w, "x")
						})
						var addr string
						if i == 0 {
							addr = startOracle(t, h, nil)
						} else {
							addr = startFast(t, h, nil)
						}
						c, err := net.DialTimeout("tcp", addr, time.Second)
						if err != nil {
							close(release)
							t.Fatal(err)
						}
						c.SetDeadline(time.Now().Add(time.Second))
						fmt.Fprintf(c, "%s / %s\r\nHost: x\r\nConnection: close\r\n\r\n", method, proto)
						br := bufio.NewReader(c)
						for {
							line, e := br.ReadString('\n')
							wires[i] = append(wires[i], line...)
							if e != nil {
								err = e
								break
							}
							if line == "\r\n" {
								break
							}
						}
						close(release)
						if err == nil {
							rest, e := io.ReadAll(br)
							err = e
							wires[i] = append(wires[i], rest...)
						}
						c.Close()
						if err != nil {
							t.Fatalf("server %d did not flush headers before release: %v", i, err)
						}
					}
					if !bytes.Equal(removeDate(wires[0]), removeDate(wires[1])) {
						t.Fatalf("oracle=%q fast=%q", wires[0], wires[1])
					}
				})
			}
		}
	}
}

func TestBufferBoundaryWire(t *testing.T) {
	for _, sizes := range [][]int{{2047}, {2048}, {2049}, {1024, 1024}, {1000, 2000}} {
		for _, encoding := range []string{"identity", "gzip"} {
			for _, stringWrite := range []bool{false, true} {
				t.Run(fmt.Sprint(sizes, stringWrite, encoding), func(t *testing.T) {
					h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						var dst io.Writer = w
						if encoding == "gzip" {
							w.Header().Set("Content-Encoding", "gzip")
							compressed := gzip.NewWriter(w)
							defer compressed.Close()
							dst = compressed
						}
						for _, n := range sizes {
							p := strings.Repeat("x", n)
							if stringWrite {
								io.WriteString(dst, p)
							} else {
								dst.Write([]byte(p))
							}
						}
					})
					req := []byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\nAccept-Encoding: " + encoding + "\r\n\r\n")
					want := removeDate(rawExchange(t, startOracle(t, h, nil), req))
					got := removeDate(rawExchange(t, startFast(t, h, nil), req))
					if !bytes.Equal(want, got) {
						t.Fatalf("wire mismatch: oracle %d bytes, fast %d bytes; oracle prefix=%q fast prefix=%q", len(want), len(got), want[:min(160, len(want))], got[:min(160, len(got))])
					}
					{
						capture := httptest.NewRecorder()
						request := httptest.NewRequest("GET", "/", nil)
						request.Header.Set("Accept-Encoding", encoding)
						h.ServeHTTP(capture, request)
						var head bytes.Buffer
						capture.Header().Write(&head)
						recorded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							w.(*response).WriteRecorded(200, head.Bytes(), capture.Body.Bytes())
						})
						replay := removeDate(rawExchange(t, startFast(t, recorded, nil), req))
						// WriteRecorded deliberately replays a single byte-slice write,
						// not the original sequence of Write/WriteString calls. Pin its
						// raw bytes against that net/http contract as well.
						single := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							for key, values := range capture.Header() {
								w.Header()[key] = values
							}
							w.Write(capture.Body.Bytes())
						})
						oneWrite := removeDate(rawExchange(t, startOracle(t, single, nil), req))
						if !bytes.Equal(oneWrite, replay) {
							t.Fatalf("single-write/recorded mismatch: oracle=%q recorded=%q", oneWrite, replay)
						}
						chunkDelta := encoding == "identity" && ((len(sizes) == 1 && sizes[0] == 2049 && stringWrite) || len(sizes) == 2 && sizes[0] == 1000)
						if !chunkDelta && !bytes.Equal(got, replay) {
							t.Fatalf("fresh/recorded mismatch: fresh=%q recorded=%q", got, replay)
						}
						if chunkDelta {
							freshHead, _, _ := bytes.Cut(got, []byte("\r\n\r\n"))
							replayHead, _, _ := bytes.Cut(replay, []byte("\r\n\r\n"))
							fresh, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(got)), nil)
							if err != nil {
								t.Fatal(err)
							}
							body, err := io.ReadAll(fresh.Body)
							fresh.Body.Close()
							if err != nil || !bytes.Equal(body, capture.Body.Bytes()) || !bytes.Equal(freshHead, replayHead) {
								t.Fatalf("delta exceeds chunk boundaries: fresh=%q recorded=%q err=%v", got, replay, err)
							}
						}
					}
				})
			}
		}
	}
}

func TestWriteFailureIsSticky(t *testing.T) {
	f := &prefixFaultConn{in: strings.NewReader(""), failAt: 1}
	c := newConn(New(http.NotFoundHandler()), f)
	r, _ := http.NewRequest("GET", "http://x/", nil)
	w := c.newResponse(r)
	first := http.NewResponseController(w).Flush()
	if first == nil {
		t.Fatal("empty flush did not report failure")
	}
	f.SetWriteDeadline(time.Time{})
	before := f.out.String()
	if _, err := w.Write([]byte("x")); err != first {
		t.Errorf("Write error=%v want %v", err, first)
	}
	if _, err := w.WriteString("x"); err != first {
		t.Errorf("WriteString error=%v want %v", err, first)
	}
	if err := http.NewResponseController(w).Flush(); err != first {
		t.Errorf("Flush error=%v want %v", err, first)
	}
	if err := w.WritePrecomposed(200, nil, nil); err != first {
		t.Errorf("precomposed error=%v want %v", err, first)
	}
	if err := w.WriteRecorded(200, nil, nil); err != first {
		t.Errorf("recorded error=%v want %v", err, first)
	}
	c.rawWrite([]byte("HTTP/1.1 103 Early Hints\r\n\r\n"))
	w.finish()
	if f.out.String() != before || f.writes != 1 {
		t.Fatalf("emission after failure: writes=%d wire=%q", f.writes, f.out.String())
	}
	if w.shouldKeepAlive() {
		t.Fatal("failed connection reusable")
	}
}
