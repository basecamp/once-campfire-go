//go:build !js

package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Batches reach real clients intact over TCP (the vectored write), whatever compression each
// side negotiated, interleaved with ordinary writes.
func TestWritePreparedBatchOverTCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	small := websocket.NewPreparedMessage(websocket.MessageText, []byte(`{"type":"welcome"}`))
	big := websocket.NewPreparedMessage(websocket.MessageText, []byte(strings.Repeat("shared message with non-ascii ☃\n", 100)))
	want := []string{`{"type":"welcome"}`, strings.Repeat("shared message with non-ascii ☃\n", 100), "interleaved"}
	for _, serverMode := range []websocket.CompressionMode{websocket.CompressionNoContextTakeover, websocket.CompressionContextTakeover, websocket.CompressionDisabled} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: serverMode, CompressionThreshold: 256})
			if err != nil {
				t.Error(err)
				return
			}
			defer c.CloseNow()
			for range 3 {
				if err := c.WritePreparedBatch(time.Now().Add(5*time.Second), []*websocket.PreparedMessage{small, big}); err != nil {
					t.Error(err)
					return
				}
				if err := c.Write(ctx, websocket.MessageText, []byte("interleaved")); err != nil {
					t.Error(err)
					return
				}
			}
			c.Close(websocket.StatusNormalClosure, "")
		}))
		for _, clientMode := range []websocket.CompressionMode{websocket.CompressionNoContextTakeover, websocket.CompressionDisabled} {
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{CompressionMode: clientMode})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 9 {
				_, got, err := c.Read(ctx)
				if err != nil || string(got) != want[i%3] {
					t.Fatalf("server %v, client %v, message %d: %v", serverMode, clientMode, i, err)
				}
			}
			if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("closing: %v", err)
			}
		}
		server.Close()
	}
}
