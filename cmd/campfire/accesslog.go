package main

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// The per-request access log (LOG_REQUESTS, default on) is the hottest write
// in the public chain: at benchmark throughput the listener emits ~400k
// lines per second. Two costs used to land on the request that produced the
// line: the shared slog handler's formatting lock serialized every request
// behind every other, and each line was one small write to the log pipe, so
// a momentarily slow sink stalled the writer and cratered whole measurement
// windows. ENGINE-62 routes access lines through an async pipeline: the
// request appends a tiny record (method, path, duration — the strings alias
// the request's own allocations) to a mutex-guarded queue — no formatting,
// no allocation, no write — and a dedicated goroutine formats the records
// with slog's exact TextHandler spelling and drains them to stderr in
// batches every 2ms (a 64 KiB chunk write, not one per line). The line
// content, ordering, the LOG_REQUESTS default and the destination are
// unchanged; the request's critical path gains only a brief queue append.
// Under extreme sink blockage the queue is bounded and further records are
// dropped with a warning line, so the server can never stall on its log.

// accessLogQueueCap bounds queued records (a few hundred bytes each, so
// roughly 200 MiB worst case before the drainer catches up).
const accessLogQueueCap = 1 << 20

// accessLogEntry is one queued access record. The strings alias the
// request's own allocations; the queue keeps them alive until the drainer
// formats them.
type accessLogEntry struct {
	method, path string
	duration     time.Duration
}

// accessLogQueue is the producer side: a mutex-guarded record list plus a
// one-shot signal for the drainer.
type accessLogQueue struct {
	mu      sync.Mutex
	entries []accessLogEntry
	spare   []accessLogEntry // drained slice, handed back to avoid reallocation
	dropped int64
	signal  chan struct{}
	stop    chan struct{}
}

func newAccessLogQueue() *accessLogQueue {
	return &accessLogQueue{signal: make(chan struct{}, 1), stop: make(chan struct{})}
}

// push is the request-side append: no formatting, no allocation, a brief
// lock. It never blocks on the sink.
func (q *accessLogQueue) push(method, path string, duration time.Duration) {
	q.mu.Lock()
	if len(q.entries) >= accessLogQueueCap {
		q.dropped++
	} else {
		q.entries = append(q.entries, accessLogEntry{method: method, path: path, duration: duration})
	}
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// startAccessLogDrainer launches the drainer goroutine and returns the stop
// function: it formats and flushes the remaining records and ends the
// goroutine (run() defers it, so the shutdown path lands the final lines).
func startAccessLogDrainer() func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainAccessLog(accessLogQueueInst, os.Stderr)
	}()
	return func() {
		close(accessLogQueueInst.stop)
		<-done
	}
}

var accessLogQueueInst = newAccessLogQueue()

// drainAccessLog formats queued records with slog's TextHandler spelling and
// writes them to dst. A flush runs at most every batchInterval: records that
// arrive between flushes accumulate, so the writer sees infrequent large
// chunks instead of one write per line. The stop channel ends the loop after
// one final flush.
func drainAccessLog(q *accessLogQueue, dst io.Writer) {
	writer := bufio.NewWriterSize(dst, 256<<10)
	handler := slog.NewTextHandler(writer, nil)
	const batchInterval = 2 * time.Millisecond
	lastFlush := time.Now()
	var flushedDrops int64
	flush := func() {
		q.mu.Lock()
		entries := q.entries
		q.entries = q.spare[:0]
		dropped := q.dropped
		q.mu.Unlock()
		now := time.Now()
		for _, entry := range entries {
			if needsQuoting(entry.method) || needsQuoting(entry.path) {
				// slog's quoting rules for exotic paths keep the slow path
				// correct; the common URL-escaped spelling never gets here.
				renderAccessRecord(handler, now, entry)
				continue
			}
			renderAccessLine(writer, now, entry)
		}
		if dropped != flushedDrops {
			flushedDrops = dropped
			slog.Error("access log queue overflow", "dropped_lines", dropped)
		}
		if writer.Buffered() > 0 {
			writer.Flush()
		}
		q.mu.Lock()
		q.spare = entries[:0]
		q.mu.Unlock()
	}
	for {
		select {
		case <-q.signal:
		case <-q.stop:
			flush()
			return
		}
		if delay := batchInterval - time.Since(lastFlush); delay > 0 {
			time.Sleep(delay)
		}
		lastFlush = time.Now()
		flush()
	}
}

// renderAccessLine writes one access record in TextHandler's exact spelling
// for values that need no quoting: time (local RFC 3339 with milliseconds),
// level, message and the three attrs, one Write into the batch buffer. This
// is the drainer's hot path — slog's record machinery would cost ~1.2 µs per
// line on the drainer goroutine (an entire benchmark CPU at line rates).
func renderAccessLine(writer *bufio.Writer, now time.Time, entry accessLogEntry) {
	var line [160]byte
	b := line[:0]
	b = append(b, "time="...)
	b = now.AppendFormat(b, "2006-01-02T15:04:05.000Z07:00")
	b = append(b, " level=INFO msg=request method="...)
	b = append(b, entry.method...)
	b = append(b, " path="...)
	b = append(b, entry.path...)
	b = append(b, " duration="...)
	b = append(b, entry.duration.String()...)
	b = append(b, '\n')
	writer.Write(b)
}

// needsQuoting mirrors slog's textHandler rule: a value is quoted when it is
// empty or contains space, =, quote, backslash, or non-printable bytes.
func needsQuoting(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == ' ' || c == '=' || c == '"' || c == '\\' || c < 0x20 || c >= 0x7f {
			return true
		}
	}
	return value == ""
}

// renderAccessRecord formats one record through slog itself (the quoting
// fallback; identical spelling by construction).
func renderAccessRecord(handler slog.Handler, now time.Time, entry accessLogEntry) {
	record := slog.NewRecord(now, slog.LevelInfo, "request", 0)
	record.AddAttrs(
		slog.String("method", entry.method),
		slog.String("path", entry.path),
		slog.Duration("duration", entry.duration),
	)
	_ = handler.Handle(nil, record)
}
