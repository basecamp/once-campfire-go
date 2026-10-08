package cable

// Test-side queue access. Simulated clients have no writer goroutine, so
// these helpers are the read side in place of the former out channel.

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// testRecvNow pops one frame without blocking; nil when the queue is empty.
func testRecvNow(c *client) *websocket.PreparedMessage {
	q := c.q
	q.mu.Lock()
	if q.len == 0 {
		q.mu.Unlock()
		return nil
	}
	f := q.frames[q.head]
	q.head = (q.head + 1) % len(q.frames)
	q.len--
	q.mu.Unlock()
	return f
}

// testRecv pops the next frame, waiting until ctx expires (returns nil
// then). The queue is checked under its own lock first, so an already-queued
// frame costs no timer.
func testRecv(ctx context.Context, c *client) *websocket.PreparedMessage {
	for {
		if f := testRecvNow(c); f != nil {
			return f
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// testQueueLen reports the number of queued frames.
func testQueueLen(c *client) int {
	c.q.mu.Lock()
	n := c.q.len
	c.q.mu.Unlock()
	return n
}

// testGrab pops one frame if any is queued, else nil. For expectNone-style
// assertions the queue length is checked first, so a stray frame is reported
// with its contents rather than drained silently.
func testGrab(t testing.TB, c *client) *websocket.PreparedMessage {
	t.Helper()
	if testQueueLen(c) == 0 {
		return nil
	}
	return testRecvNow(c)
}

// testRecvTimeout pops the next frame, failing the test after 10s.
func testRecvTimeout(t testing.TB, c *client) *websocket.PreparedMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := testRecv(ctx, c)
	if f == nil {
		t.Fatal("missing delivery")
	}
	return f
}
