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

// Prepared frames are written with one vectored write; every payload length
// encoding (7-bit, 16-bit, 64-bit) must reach both compressed and plain clients.
func TestPreparedFrameLengths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var payloads []string
	for _, n := range []int{0, 1, 125, 126, 127, 4095, 4096, 4097, 65535, 65536, 70000} {
		payloads = append(payloads, strings.Repeat("x", n))
	}
	payloads = append(payloads, strings.Repeat("ünïcödé ☃ ", 9000))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover, CompressionThreshold: 256})
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		var batch []*websocket.PreparedMessage
		for _, payload := range payloads {
			if err := connection.WritePrepared(ctx, websocket.NewPreparedMessage(websocket.MessageText, []byte(payload))); err != nil {
				t.Error(err)
				return
			}
			if err := connection.Write(ctx, websocket.MessageText, []byte("interleaved")); err != nil {
				t.Error(err)
				return
			}
			batch = append(batch, websocket.NewPreparedMessage(websocket.MessageText, []byte(payload)), websocket.NewPreparedMessage(websocket.MessageText, []byte("interleaved")))
		}
		// The same messages again as one batch of frames.
		if err := connection.WritePrepared(ctx, batch...); err != nil {
			t.Error(err)
			return
		}
		connection.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()
	for _, compression := range []websocket.CompressionMode{websocket.CompressionDisabled, websocket.CompressionNoContextTakeover} {
		connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{CompressionMode: compression})
		if err != nil {
			t.Fatal(err)
		}
		connection.SetReadLimit(1 << 20)
		for range 2 {
			for _, payload := range payloads {
				for _, want := range []string{payload, "interleaved"} {
					_, got, err := connection.Read(ctx)
					if err != nil || string(got) != want {
						t.Fatalf("compression %v, %d bytes: %d bytes, %v", compression, len(want), len(got), err)
					}
				}
			}
		}
		connection.CloseNow()
	}
}
