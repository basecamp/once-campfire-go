package fastserve

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// The one-byte read is the observer, never the head/body buffered reader.
type observerProbeConn struct {
	net.Conn
	started  chan struct{}
	finished chan struct{}
}

func (c *observerProbeConn) Read(p []byte) (int, error) {
	if len(p) != 1 {
		return c.Conn.Read(p)
	}
	c.started <- struct{}{}
	n, err := c.Conn.Read(p)
	c.finished <- struct{}{}
	return n, err
}

func awaitObserver(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("observer did not finish within one second")
	}
}

func TestObserverPipelineReplay(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	p := &observerProbeConn{server, make(chan struct{}, 4), make(chan struct{}, 4)}
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			<-release
			if r.Context().Err() != nil {
				t.Error("pipelined data canceled request")
			}
		}
		io.WriteString(w, r.URL.Path)
	}))
	c := newConn(s, p)
	s.wg.Add(1)
	ended := make(chan struct{})
	go func() { c.serve(); close(ended) }()
	if _, err := io.WriteString(client, "GET /first HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	awaitObserver(t, p.started)
	// Force the observer, rather than the head parser, to take the first byte.
	if _, err := io.WriteString(client, "G"); err != nil {
		t.Fatal(err)
	}
	awaitObserver(t, p.finished)
	close(release)
	br := bufio.NewReader(client)
	readResponse := func(want string) {
		t.Helper()
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || string(body) != want {
			t.Fatalf("response %d %q: %v", res.StatusCode, body, err)
		}
	}
	readResponse("/first")
	if _, err := io.WriteString(client, "ET /second HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	readResponse("/second")
	awaitObserver(t, ended)
	select {
	case <-c.observer.exited:
	default:
		t.Fatal("worker survived connection completion")
	}
}

func TestObserverAbortJoinsRead(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := &observerProbeConn{server, make(chan struct{}, 1), make(chan struct{}, 1)}
	c := newConn(New(nil), p)
	c.setReadDeadline(time.Unix(1, 0)) // body deadline must not carry into observation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.observer.cancel = cancel
	c.observer.start()
	awaitObserver(t, p.started)
	stopped := make(chan struct{})
	go func() { c.observer.stop(); close(stopped) }()
	awaitObserver(t, stopped)
	awaitObserver(t, p.finished)
	if ctx.Err() != nil {
		t.Fatal("intentional deadline interrupt canceled context")
	}
	if c.observer.active || c.readArmed {
		t.Fatal("observer read or interrupt deadline survived stop")
	}
	c.observer.close()
	awaitObserver(t, c.observer.exited)
}

func TestDisconnectProbe(t *testing.T) {
	for _, request := range []string{
		"GET / HTTP/1.1\r\nHost: x\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\n\r\nabc",
		"GET / HTTP/1.1\r\nHost: x\r\n\r\nGET /next HTTP/1.1\r\nHost: x\r\n\r\n",
	} {
		for _, oracle := range []bool{true, false} {
			t.Run(request[:4]+map[bool]string{true: "oracle", false: "fast"}[oracle], func(t *testing.T) {
				started := make(chan struct{})
				canceled := make(chan struct{})
				release := make(chan struct{})
				defer close(release)
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/next" {
						return
					}
					io.Copy(io.Discard, r.Body)
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-release:
					}
				})
				var addr string
				if oracle {
					addr = startOracle(t, h, nil)
				} else {
					addr = startFast(t, h, nil)
				}
				c, err := net.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				c.SetDeadline(time.Now().Add(time.Second))
				io.WriteString(c, request)
				select {
				case <-started:
				case <-time.After(time.Second):
					c.Close()
					t.Fatal("handler did not start")
				}
				c.Close()
				select {
				case <-canceled:
				case <-time.After(500 * time.Millisecond):
					t.Fatal("disconnect did not cancel context during handler")
				}
			})
		}
	}
}

func TestDisconnectBodyCompletion(t *testing.T) {
	for _, mode := range []string{"exact-length", "close-drain", "chunked"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			addr := startFast(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				switch mode {
				case "exact-length":
					var b [3]byte
					_, err = io.ReadFull(r.Body, b[:])
				case "close-drain":
					err = r.Body.Close()
				default:
					_, err = io.Copy(io.Discard, r.Body)
				}
				if err != nil {
					t.Errorf("consume body: %v", err)
				}
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
			}), nil)
			c, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(time.Second))
			request := "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\n\r\nabc"
			if mode == "chunked" {
				request = "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n"
			}
			if _, err := io.WriteString(c, request); err != nil {
				t.Fatal(err)
			}
			awaitObserver(t, started)
			c.Close()
			awaitObserver(t, canceled)
		})
	}
}
