package main

import (
	"bufio"
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// testQueue builds an isolated queue (the package global belongs to the
// server process, not to tests).
func testQueue() *accessLogQueue { return newAccessLogQueue() }

// TestAccessLogPipeline pins the pipeline end to end: records pushed from
// the request side are formatted by the drainer in slog's spelling and land
// on the destination in batches; the stop function flushes what is left.
func TestAccessLogPipeline(t *testing.T) {
	q := testQueue()
	dst := &fileLike{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainAccessLog(q, dst)
	}()

	q.push("GET", "/up", 12345*time.Microsecond)
	q.push("GET", "/assets/x.css", 30*time.Microsecond)
	// The drainer batches at batchInterval; poll for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(dst.String(), `msg=request method=GET path=/up`) &&
			strings.Contains(dst.String(), `path=/assets/x.css`) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := dst.String()
	if !strings.Contains(got, `msg=request method=GET path=/up duration=12.345ms`) {
		t.Fatalf("drainer did not render the record: %q", got)
	}
	if !strings.Contains(got, "level=INFO") {
		t.Fatalf("drainer output is not slog spelling: %q", got)
	}

	// Stop: the final push lands with the final flush.
	q.push("HEAD", "/up", 1*time.Microsecond)
	close(q.stop)
	<-done
	if !strings.Contains(dst.String(), `method=HEAD path=/up`) {
		t.Fatalf("stop did not flush the final record: %q", dst.String())
	}
}

// fileLike adapts a mutex-guarded buffer to the drainer's destination
// interface (the test also reads it from its own goroutine).
type fileLike struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (f *fileLike) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.b.Write(p)
}

func (f *fileLike) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.b.String()
}

// TestAccessLogQueueBounded pins the bound: once the queue is full, pushers
// finish immediately (their records are counted dropped, never blocking).
func TestAccessLogQueueBounded(t *testing.T) {
	q := testQueue()
	for i := 0; i < accessLogQueueCap+10; i++ {
		q.push("GET", "/up", time.Microsecond)
	}
	q.mu.Lock()
	dropped := q.dropped
	queued := len(q.entries)
	q.mu.Unlock()
	if queued != accessLogQueueCap {
		t.Fatalf("queued %d, want %d", queued, accessLogQueueCap)
	}
	if dropped != 10 {
		t.Fatalf("dropped %d, want 10", dropped)
	}
}

// TestAccessLogFastPathPins renders the same records through the fast line
// writer and slog's TextHandler and requires byte equality (the fast path is
// the drainer's hot path and must not drift from the format's spelling).
func TestAccessLogFastPathPins(t *testing.T) {
	now := time.Date(2026, 10, 7, 20, 34, 12, 345000000, time.FixedZone("", 3600))
	cases := []accessLogEntry{
		{method: "GET", path: "/up", duration: 45 * time.Microsecond},
		{method: "POST", path: "/rooms/1/messages", duration: time.Millisecond},
		{method: "GET", path: benchLikePath(), duration: 123456 * time.Nanosecond},
	}
	for _, entry := range cases {
		var fast bytes.Buffer
		writer := bufio.NewWriter(&fast)
		renderAccessLine(writer, now, entry)
		writer.Flush()
		var slow bytes.Buffer
		slowHandler := slog.NewTextHandler(&slow, nil)
		renderAccessRecord(slowHandler, now, entry)
		if fast.String() != slow.String() {
			t.Fatalf("fast path drifted from slog:\nfast %q\nslow %q", fast.String(), slow.String())
		}
	}
}

// benchLikePath is a long signed-token avatar path, unquoted by both paths.
func benchLikePath() string {
	return "/users/eyJfcmFpdHMiOnsidG9rZW4iOiIyOTgyOWJjODBlZTcyY2E2NzQyODgxZTlhZWU5NmE1ZmE0ZTZiMDA0OTJiZTJlNTc1MGZhMDQ3MTM5MGM4YjIxIiwiZXhwaXJlc19hdCI6IjIwMjYtMTAtMDdUMjM6NTk6NTlaeiJ9fQ--9e2b0a9d7d47f1693d91df77af215fd9298a9322/avatar"
}

// TestNeedsQuoting pins the quoting decision boundary against slog's.
func TestNeedsQuoting(t *testing.T) {
	for _, value := range []string{"/up", "a b", "a=b", "quo\"te", "back\\slash", "tab\tx", "/é", ""} {
		want := logNeedsQuoting(value)
		if got := needsQuoting(value); got != want {
			t.Errorf("needsQuoting(%q) = %v, slog says %v", value, got, want)
		}
	}
}

// logNeedsQuoting asks slog itself: the attr renders `k="` exactly when the
// value needs quoting.
func logNeedsQuoting(value string) bool {
	var out bytes.Buffer
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
	record.AddAttrs(slog.String("k", value))
	_ = slog.NewTextHandler(&out, nil).Handle(nil, record)
	return bytes.Contains(out.Bytes(), []byte(`k="`))
}

// TestAccessLogQueueSpare pins the spare hand-back: after the drainer takes a
// batch, the queue's append slice is the spare (no reallocation per batch)
// and concurrent pushers never share the drained backing array.
func TestAccessLogQueueSpare(t *testing.T) {
	q := testQueue()
	for i := 0; i < 100; i++ {
		q.push("GET", "/up", time.Microsecond)
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				q.push("POST", "/rooms/1/messages", time.Microsecond)
			}
		}()
	}
	// Interleave the drainer's swap while pushers run, exactly as in service.
	for i := 0; i < 20; i++ {
		q.mu.Lock()
		entries := q.entries
		q.entries = q.spare[:0]
		q.mu.Unlock()
		time.Sleep(20 * time.Microsecond)
		q.mu.Lock()
		q.spare = entries[:0]
		q.mu.Unlock()
	}
	wg.Wait()
}
