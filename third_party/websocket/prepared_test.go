//go:build !js

package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSharedPreparedFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := []byte(strings.Repeat("shared message with non-ascii ☃\n", 100))
	expected := string(payload)
	prepared := websocket.NewPreparedMessage(websocket.MessageText, payload)
	payload[0] = '!'
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover, CompressionThreshold: 256})
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		for i := 0; i < 4; i++ {
			if err := connection.WritePrepared(ctx, prepared); err != nil {
				t.Error(err)
				return
			}
			if err := connection.Write(ctx, websocket.MessageText, []byte("interleaved")); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer server.Close()
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			compression := websocket.CompressionNoContextTakeover
			if i%2 == 0 {
				compression = websocket.CompressionDisabled
			}
			connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{CompressionMode: compression})
			if err != nil {
				t.Error(err)
				return
			}
			defer connection.CloseNow()
			for j := 0; j < 4; j++ {
				for _, want := range []string{expected, "interleaved"} {
					_, got, err := connection.Read(ctx)
					if err != nil || string(got) != want {
						t.Errorf("prepared frame mismatch: %v", err)
						return
					}
				}
			}
		}()
	}
	group.Wait()
}
