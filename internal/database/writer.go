package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

// ErrWriterClosed is returned when a message job is submitted after the write
// queue has been closed (shutdown). The message did not persist.
var ErrWriterClosed = errors.New("database: writer queue closed")

// writeQueueCapacity bounds the queued message jobs; senders block when the
// queue is full, which is the backpressure the single writer connection needs
// (Rust's own write queue is bounded the same way).
const writeQueueCapacity = 1024

// messageWriter serializes message creation onto the one write connection and
// commits every job it drains in a single transaction: group commit. Each job
// runs inside its own savepoint, so a job that fails rolls back exactly its
// own statements while the rest of the batch still commits; the commit itself
// is one per batch. After the shared commit the requesting goroutine runs the
// job's after-commit work (search index, unread bump) — the same statements
// the Rust port runs in its after_commit hooks.
//
// A writer is not safe for direct use; all jobs go through submit, and Close
// must be called before the underlying connection closes.
type messageWriter struct {
	db   *sql.DB
	jobs chan *messageJob
	stop chan struct{}
	done chan struct{}
	once sync.Once
	// mu guards closed and inflight. closed rejects submissions after Close;
	// inflight counts submissions that passed the closed check and are still
	// between their mutex unlock and the channel send, so Close's drain knows
	// when no send can land any more.
	mu       sync.Mutex
	closed   bool
	inflight int
}

type messageJob struct {
	ctx   context.Context
	run   func(*sql.Tx) (Message, error)
	after func(context.Context, Message) error
	done  chan messageResult
}

type messageResult struct {
	message Message
	err     error
}

func newMessageWriter(db *sql.DB) *messageWriter {
	w := &messageWriter{
		db:   db,
		jobs: make(chan *messageJob, writeQueueCapacity),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go w.loop()
	return w
}

func (w *messageWriter) submit(job *messageJob) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWriterClosed
	}
	w.inflight++
	w.mu.Unlock()
	w.jobs <- job
	w.mu.Lock()
	w.inflight--
	w.mu.Unlock()
	return nil
}

// Close stops the loop after it has drained the queue: every job submitted
// before Close ran to completion (submitted after returns ErrWriterClosed).
// Close blocks until the queue is empty and the loop has exited. It is
// idempotent.
func (w *messageWriter) Close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		close(w.stop)
		<-w.done
	})
}

func (w *messageWriter) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			w.drain()
			return
		case job := <-w.jobs:
			w.runBatch(job)
		}
	}
}

// drain processes every job still queued; it is the shutdown path and waits
// for submissions that passed the closed check but had not sent yet (their
// count is inflight, and their send lands in the buffered channel).
func (w *messageWriter) drain() {
	for {
		select {
		case job := <-w.jobs:
			w.runBatch(job)
		default:
			w.mu.Lock()
			n := w.inflight
			w.mu.Unlock()
			if n == 0 {
				return
			}
			runtime.Gosched()
		}
	}
}

// runBatch commits the head job and everything queued behind it in one
// transaction. Results are delivered only after the commit, so a caller's
// response can never precede its message's persistence.
func (w *messageWriter) runBatch(head *messageJob) {
	batch := []*messageJob{head}
	for {
		select {
		case job := <-w.jobs:
			batch = append(batch, job)
		default:
			goto run
		}
	}
run:
	results := make([]messageResult, len(batch))
	var commitErr error
	var tx *sql.Tx
	func() {
		// A panicking job must not take the writer down with it; its batch is
		// rolled back and every job in it fails, like the legacy path.
		defer func() {
			if p := recover(); p != nil {
				slog.Error("message write batch panicked", "error", p)
				commitErr = fmt.Errorf("database: message write panic: %v", p)
				if tx != nil {
					tx.Rollback()
				}
			}
		}()
		var err error
		tx, err = w.db.BeginTx(context.Background(), nil)
		if err != nil {
			commitErr = err
			return
		}
		for i, job := range batch {
			if err := job.ctx.Err(); err != nil {
				// Cancelled before execution: nothing ran, nothing persists,
				// exactly like the legacy BeginTx failing on a cancelled ctx.
				results[i] = messageResult{err: err}
				continue
			}
			if _, err := tx.Exec("SAVEPOINT w"); err != nil {
				results[i] = messageResult{err: err}
				continue
			}
			m, err := job.run(tx)
			if err != nil {
				tx.Exec("ROLLBACK TO w")
				tx.Exec("RELEASE w")
				results[i] = messageResult{err: err}
				continue
			}
			if _, err := tx.Exec("RELEASE w"); err != nil {
				results[i] = messageResult{err: err}
				continue
			}
			results[i] = messageResult{message: m}
		}
		if err := tx.Commit(); err != nil {
			commitErr = err
		}
	}()
	for i, job := range batch {
		if result := results[i]; result.err == nil && commitErr != nil {
			// The commit failed: no statement of the batch persisted, so the
			// job's own savepoint work is gone with it.
			results[i] = messageResult{err: commitErr}
		}
		job.done <- results[i]
	}
}

// checkpointer runs PASSIVE WAL checkpoints on its own connection, off the
// writer. The writer connection has PRAGMA wal_autocheckpoint=0, so no commit
// ever pays the checkpoint's WAL and database fsyncs; the checkpointer pays
// them on a schedule instead, exactly the division of labour the Rust port's
// checkpointer thread implements.
type checkpointer struct {
	conn     *sql.DB
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func startCheckpointer(dsn string, interval time.Duration) (*checkpointer, error) {
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	c := &checkpointer{conn: conn, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
	go c.loop()
	return c, nil
}

func (c *checkpointer) loop() {
	defer close(c.done)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.passive()
		}
	}
}

func (c *checkpointer) passive() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// PASSIVE never blocks: frames a reader is using are skipped and picked up
	// on the next tick. The row reports (busy, log pages, checkpointed pages).
	var busy, log, checked int
	if err := c.conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &log, &checked); err != nil {
		slog.Warn("WAL checkpoint failed", "error", err)
	}
}

func (c *checkpointer) Close() {
	c.once.Do(func() {
		close(c.stop)
		<-c.done
		c.passive() // one final checkpoint after the writer drained
		c.conn.Close()
	})
}
