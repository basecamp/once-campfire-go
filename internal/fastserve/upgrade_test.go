package fastserve

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestWebSocketRoundtripThroughLoop is the real upgrade handoff: the client
// handshake arrives at this loop, the head's Upgrade header sends the
// connection to the internal net/http server, the handler hijacks it, and
// WebSocket frames flow both ways. This is the exact path WebCable uses
// (websocket.Accept in internal/cable).
func TestWebSocketRoundtripThroughLoop(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer c.CloseNow()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), websocket.MessageText, data); err != nil {
				return
			}
		}
	})
	addr := startFast(t, handler, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatalf("websocket dial through the loop: %v", err)
	}
	defer c.CloseNow()
	for i := 0; i < 10; i++ {
		msg := "ping"
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		_, data, err := c.Read(ctx)
		if err != nil || string(data) != msg {
			t.Fatalf("echo %d: %q err %v", i, data, err)
		}
	}
	// A second (keep-alive) WebSocket on the same server, to prove the
	// handoff path handles concurrent upgrades.
	c2, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer c2.CloseNow()
	if err := c2.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, data, err := c2.Read(ctx); err != nil || string(data) != "hi" {
		t.Fatalf("second echo: %q err %v", data, err)
	}
}

// TestH2CThroughHandoff: a client opening with the HTTP/2 preface must be
// handed to the internal net/http server, which switches protocols and
// serves h2c. The loop can never speak h2 itself; the handoff is the only
// path. (The stdlib client uses h2c prior-knowledge only when its Protocols
// exclude HTTP/1.)
func TestH2CThroughHandoff(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "proto="+r.Proto)
	})
	addr := startFast(t, handler, func(s *Server) {
		p := new(http.Protocols)
		p.SetHTTP1(true)
		p.SetHTTP2(true)
		p.SetUnencryptedHTTP2(true)
		s.Protocols = p
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true) // no HTTP1: prior-knowledge h2c
	client := &http.Client{Transport: &http.Transport{Protocols: protocols}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("h2c request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 2 {
		t.Fatalf("proto %d.%d, want HTTP/2 through the handoff", resp.ProtoMajor, resp.ProtoMinor)
	}
	if string(body) != "proto=HTTP/2.0" {
		t.Fatalf("body %q", body)
	}
}

// TestPrefaceWithoutH2 parities the case where the loop's protocols do NOT
// include h2c (the internal target listener's configuration): the preface
// still hands the connection off, and the internal net/http server treats it
// as the plain "PRI * HTTP/2.0" request net/http always has there. Both
// loops must answer identically.
func TestPrefaceWithoutH2(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Method+" "+r.RequestURI+" "+r.Proto)
	})
	oracle := startOracle(t, handler, nil) // net/http without h2
	fast := startFast(t, handler, nil)
	for _, addr := range []string{oracle, fast} {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		c.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
		buf, err := io.ReadAll(c)
		c.Close()
		if err != nil {
			t.Fatalf("preface read: %v", err)
		}
		if !strings.Contains(string(buf), "PRI * HTTP/2.0") {
			t.Fatalf("preface answer %q", buf)
		}
	}
}

// TestPrefaceFragmented sends the preface one byte at a time; the loop must
// neither 400 a fragment nor stall, and must deliver the same answer as a
// single-shot preface.
func TestPrefaceFragmented(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Method)
	})
	fast := startFast(t, handler, nil)
	c, err := net.DialTimeout("tcp", fast, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < len(h2Preface); i++ {
		if _, err := c.Write(h2Preface[i : i+1]); err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	buf, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(buf), "PRI") {
		t.Fatalf("fragmented preface answer %q", buf)
	}
}
