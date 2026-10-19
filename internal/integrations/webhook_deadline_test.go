package integrations

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookReadDeadlineRefreshOnReuse(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			const readTimeout = 800 * time.Millisecond
			const idle = 600 * time.Millisecond
			const responseDelay = 400 * time.Millisecond
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if r.URL.Path == "/slow" {
					timer := time.NewTimer(responseDelay)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-r.Context().Done():
						return
					}
				}
				io.WriteString(w, "ok")
			}))
			if secure {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			client := WebhookHTTPClient(readTimeout, nil)
			transport := client.Transport.(rubyTransport).next.(*http.Transport)
			if secure {
				transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			}
			defer transport.CloseIdleConnections()
			var traceCalls atomic.Int64
			post := func(path string) (bool, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var reused atomic.Bool
				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
					traceCalls.Add(1)
					reused.Store(info.Reused)
				}})
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader("{}"))
				if err != nil {
					return false, err
				}
				response, err := client.Do(req)
				if err != nil {
					return reused.Load(), err
				}
				defer response.Body.Close()
				_, err = io.Copy(io.Discard, response.Body)
				return reused.Load(), err
			}
			if _, err := post("/quick"); err != nil {
				t.Fatal(err)
			}
			time.Sleep(idle)
			reused, err := post("/slow")
			if err != nil {
				t.Fatalf("response delayed %s on a reused connection with timeout %s: %v", responseDelay, readTimeout, err)
			}
			if !reused {
				t.Fatal("second POST did not reuse its connection")
			}
			if got := traceCalls.Load(); got != 2 {
				t.Fatalf("caller GotConn hook called %d times, want 2", got)
			}
		})
	}
}

func TestWebhookReadDeadlineStillBoundsResponseBody(t *testing.T) {
	const readTimeout = 200 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, "start")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := WebhookHTTPClient(readTimeout, nil)
	transport := client.Transport.(rubyTransport).next.(*http.Transport)
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("stalled body must time out: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("body used the outer context deadline instead of the socket timeout: %v", ctx.Err())
	}
}
