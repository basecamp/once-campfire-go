package cable

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Real socket backpressure exercises expiration rather than queue overflow.
// Successful writes must disarm the deadline while idle; expiration must close
// blocked I/O and be terminal even if another write is attempted immediately.
func TestFrameWriterDeadlineLifecycle(t *testing.T) {
	next := make(chan struct{})
	result := make(chan error, 1)
	limit := 100 * time.Millisecond
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		writer := newFrameWriter(ctx, cancel, conn, limit)
		defer writer.Close()
		for _, text := range []string{"one", "two"} {
			if err := writer.Write(websocket.NewPreparedMessage(websocket.MessageText, []byte(text))); err != nil {
				result <- err
				return
			}
			<-next
		}
		err = writer.Write(websocket.NewPreparedMessage(websocket.MessageText, []byte(strings.Repeat("x", 256<<10))))
		if !errors.Is(err, context.DeadlineExceeded) {
			result <- err
			return
		}
		if err = writer.Write(websocket.NewPreparedMessage(websocket.MessageText, []byte("after expiration"))); !errors.Is(err, context.Canceled) {
			result <- err
			return
		}
		result <- context.DeadlineExceeded
	}))
	server.Config.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if err := conn.(*net.TCPConn).SetWriteBuffer(1024); err != nil {
			t.Error(err)
		}
		return ctx
	}
	server.Start()
	defer server.Close()
	defer close(next)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err == nil {
			if err = conn.(*net.TCPConn).SetReadBuffer(1024); err != nil {
				conn.Close()
				return nil, err
			}
		}
		return conn, err
	}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, text := range []string{"one", "two"} {
		_, data, err := conn.Read(ctx)
		if err != nil || string(data) != text {
			t.Fatalf("write %s: %q %v", text, data, err)
		}
		time.Sleep(2 * limit) // Idle longer than the per-write limit.
		next <- struct{}{}
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked writer did not expire terminally: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("write deadline did not interrupt socket backpressure")
	}
}
