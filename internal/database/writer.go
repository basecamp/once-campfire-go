package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ErrWriterClosed is returned when a message job is submitted after the write
// queue has been closed (shutdown). The message did not persist.
var ErrWriterClosed = errors.New("database: writer queue closed")

// writeQueueCapacity bounds the queued message jobs; senders block when the
// queue is full, which is the backpressure the single writer connection needs
// (Rust's own write queue is bounded the same way).
const writeQueueCapacity = 1024

// afterBatchLimit bounds one optional after-phase transaction. Production
// message creation commits search, unread and push intent in the primary phase.
// At most this many optional callbacks share a commit. It caps the
// statements and WAL frames a single after transaction writes and the time
// the writer holds the lane for one chunk, while keeping a burst's commit
// count near two: a full queue-capacity batch (writeQueueCapacity jobs) needs
// four after transactions, and any batch at or below this bound needs one.
// Larger batches split into several after transactions.
const afterBatchLimit = 256

// writerBatchObserver, when non-nil, is captured by newMessageWriter and
// called with each primary batch's size and its after-phase transaction
// count. It is a test hook, set before Open (like fastWriteRecord), for the
// batch-size histogram and the commits-per-message contract.
var writerBatchObserver func(primary, afterChunksAttempted int)

// messageWriter serializes message creation onto the one write connection.
//
// The lane is adaptive (ENGINE-45): a job submitted while the queue is empty
// and no batch is in flight runs in-line on the caller's goroutine, in its
// own transaction — no channel hop, no extra scheduling latency, one commit
// (the same shape as the direct CAMPFIRE_WRITE_QUEUE=off path). Any job that
// arrives while a write is in flight is queued instead, and the loop commits
// every job it drains in a single transaction: group commit. The busy mark
// is set and cleared in the same critical section that takes a job (submit
// for in-line, the loop for batches), and in-line claims additionally
// require an empty queue, so a later write can never overtake an earlier
// one: ids ascend with the order the jobs are taken.
//
// On the queued path each job runs inside its own savepoint, so a job that
// fails rolls back exactly its own statements while the rest of the batch
// still commits; the commit itself is one per batch. Search, unread and push
// intent belong to this primary transaction. For jobs with optional after
// callbacks, the writer runs them in bounded transactions, each job inside its
// own savepoint when the transaction holds more than one, so one failed
// after-job rolls back only its own statements while the rest commit. A batch
// with optional callbacks therefore costs the primary commit plus the after phase's
// transactions — two when the batch fits one after transaction — instead of
// the B+1 commits the per-caller shape paid. The in-line path folds the
// after work into the job's own transaction, so a lone post commits
// atomically; its after-commit-failure behaviour is the direct path's (the
// write rolls back with the failing statement), not the queued path's.
//
// A writer is not safe for direct use; all jobs go through submit, and Close
// must be called before the underlying connection closes.
type messageWriter struct {
	conn laneConn
	jobs chan *messageJob
	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
	// observe, when non-nil (tests), receives each primary batch's size and
	// the number of after-phase transactions it needed. It is captured at
	// construction from writerBatchObserver, so a test sets that before Open.
	observe func(primary, afterChunksAttempted int)
	// mu guards closed, inflight and busy. closed rejects submissions after
	// Close; inflight counts submissions that passed the closed check and
	// are still between their mutex unlock and the channel send, so Close's
	// drain knows when no send can land any more; busy marks the writer as
	// executing — a batch on the loop or an in-line job on a caller — and is
	// written under mu together with the job that made the writer busy.
	mu       sync.Mutex
	closed   bool
	inflight int
	busy     bool
	// limit is the WAL-growth threshold in frames (CAMPFIRE_CHECKPOINT_PAGES);
	// 0 disables it. gate serializes a restart with the background
	// checkpointer (SQLite allows one checkpoint at a time, and a RESTART that
	// arrives while another checkpoint holds the lock is refused).
	//
	// The counters are the telemetry. restartInvocations counts checks that
	// decided to try (the threshold was reached, the gate was free and the
	// backoff window had elapsed); restartAttempts counts those whose lane
	// actually ran the checkpoint pragmas (an unusable connection returns
	// attempted=false without running them); restartErrors counts invocations
	// that returned an error (including cleanup after a successful RESTART)
	// or never attempted; this is an invocation failure, not proof that SQLite
	// failed to restart. restarts counts wholly successful invocations where
	// SQLite completed RESTART; restartIncomplete counts attempts it
	// refused, reported in the pragma's result row; restartDeferred counts
	// checks skipped because the checkpointer held the gate or the backoff
	// window had not elapsed. The stall counters measure the wall time the
	// writer spent inside an invocation, split into the backfill step
	// (copy + sync) and the RESTART step (which includes the busy-handler
	// wait), which is what lets the busy-wait setting be judged separately
	// from the checkpoint's own I/O.
	limit               int64
	gate                *checkpointGate
	restartInvocations  atomic.Int64
	restartAttempts     atomic.Int64
	restartErrors       atomic.Int64
	restarts            atomic.Int64
	restartIncomplete   atomic.Int64
	restartDeferred     atomic.Int64
	restartStallNanos   atomic.Int64
	restartStallMax     atomic.Int64
	restartPassiveNanos atomic.Int64
	restartRestartNanos atomic.Int64
	// restartAfter and restartBackoff are the incomplete-attempt backoff. They
	// are plain fields because walLimit runs only under the writer's busy
	// protocol (the loop or the single in-line claim), never concurrently.
	restartAfter   time.Time
	restartBackoff time.Duration
	telemetry      bool
}

// Backoff between restart attempts after SQLite refuses one. A refused restart
// means a reader is still pinning frames; retrying immediately only stalls the
// writer again, so attempts are spaced out, doubling to the cap. The attempt
// itself is bounded by write.CheckpointBusyMS, so a pinned reader can hold the
// writer for at most that long per attempt, at most once per backoff window.
const (
	restartBackoffMin = 25 * time.Millisecond
	restartBackoffMax = 2 * time.Second
)

// checkpointGate serializes WAL checkpoints between the write lane and the
// background checkpointer. SQLite runs one checkpoint at a time: a RESTART
// that arrives while the checkpointer holds the lock is refused (reported busy
// in its result row), which would look like a pinned reader. The writer
// TryLocks so it never queues behind a scheduled checkpoint — it defers to the
// next drain instead — while the checkpointer Locks, so its checkpoint waits
// for a bounded writer restart rather than being refused.
type checkpointGate struct{ mu sync.Mutex }

func (g *checkpointGate) try() bool { return g.mu.TryLock() }
func (g *checkpointGate) lock()     { g.mu.Lock() }
func (g *checkpointGate) unlock()   { g.mu.Unlock() }

type messageJob struct {
	ctx   context.Context
	run   func(jobTx) (Message, error)
	after func(context.Context, jobTx, Message) error
	done  chan messageResult
}

type messageResult struct {
	message Message
	err     error
	// afterErr is the queued path's after-commit failure: the primary commit
	// landed and the message is durable, but its optional callback did
	// not. The caller reports it the way Rails raises from the save that
	// committed — the message stays. It is always nil on the direct and
	// in-line paths, whose after work commits with the row.
	afterErr error
}

func newMessageWriter(conn laneConn, limit int64, gate *checkpointGate) *messageWriter {
	w := &messageWriter{
		conn:      conn,
		jobs:      make(chan *messageJob, writeQueueCapacity),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		observe:   writerBatchObserver,
		limit:     limit,
		gate:      gate,
		telemetry: checkpointTelemetry(),
	}
	go w.loop()
	return w
}

// walLimit is the writer's WAL-growth threshold (the Rust port's
// WAL_LIMIT_PAGES and its writer-side restart_wal, without the per-message
// hop): once the WAL has grown to limit frames, the writer backfills and
// restarts it between jobs so the next commit resets the WAL to its first
// frame. The frame count comes from the lane's WAL hook — the fastdb lane
// takes its mutex and makes one cgo call — so the check runs once per drain,
// not per message and not per statement, and does nothing at all when the
// threshold is disabled or the WAL is below it. The database/sql lane, which
// has no hook, probes with a throttled NOOP pragma.
//
// The threshold is a threshold, not a bound: the check runs after a drain, so
// a batch (or a run of drains) can overshoot it by whatever it wrote between
// checks, a single large message can add many frames at once, and a foreign
// writer's commits are invisible to this connection's hook. There is no
// backpressure: the writer never refuses or delays a write to stay under the
// threshold.
//
// Nor does the threshold bound growth in every state. A RESTART cannot reset
// the WAL while a reader holds a WAL read slot, so with continuing writes and
// a persistently pinned reader the WAL grows without bound until that reader
// goes away — the backoff deliberately permits that rather than stalling the
// writer on every drain. The threshold bounds growth only while restarts can
// complete.
func (w *messageWriter) walLimit() {
	if w.limit <= 0 {
		return
	}
	frames := w.conn.walFrames()
	if frames < w.limit {
		return
	}
	if !w.restartAfter.IsZero() && time.Now().Before(w.restartAfter) {
		w.restartDeferred.Add(1)
		return
	}
	if w.gate != nil {
		if !w.gate.try() {
			w.restartDeferred.Add(1)
			return
		}
		defer w.gate.unlock()
	}
	start := time.Now()
	outcome, err := w.conn.backfillRestart()
	stall := time.Since(start)
	w.restartInvocations.Add(1)
	if outcome.attempted {
		w.restartAttempts.Add(1)
	}
	w.restartStallNanos.Add(stall.Nanoseconds())
	updateMax(&w.restartStallMax, stall.Nanoseconds())
	w.restartPassiveNanos.Add(outcome.passiveNanos)
	w.restartRestartNanos.Add(outcome.restartNanos)
	if err != nil || !outcome.attempted {
		// Invocation error, possibly cleanup after SQLite completed RESTART.
		// Keep the existing backoff policy; report completion separately.
		w.restartErrors.Add(1)
		if err == nil {
			err = errRestartNotAttempted
		}
		slog.Warn("WAL checkpoint invocation failed", "error", err, "attempted", outcome.attempted, "sqlite_restart_completed", outcome.completed)
		w.backoffRestart()
		return
	}
	if !outcome.completed {
		// SQLite refused the restart: a reader is still pinning frames (or
		// another checkpoint won the lock). The WAL is not reset and the next
		// commit will not restart it either, so space the next attempt out
		// instead of stalling the writer again immediately.
		w.restartIncomplete.Add(1)
		w.backoffRestart()
		if w.telemetry {
			slog.Info("wal restart incomplete",
				"frames", outcome.frames, "backfilled", outcome.backfilled,
				"limit", w.limit, "stall_us", stall.Microseconds(), "backoff_ms", w.restartBackoff.Milliseconds())
		}
		return
	}
	w.restarts.Add(1)
	w.restartBackoff = 0
	w.restartAfter = time.Time{}
	if w.telemetry {
		slog.Info("wal restart", "frames", frames, "limit", w.limit,
			"stall_us", stall.Microseconds(),
			"passive_us", outcome.passiveNanos/1000, "restart_us", outcome.restartNanos/1000)
	}
}

// errRestartNotAttempted is reported when a lane returns without running the
// checkpoint pragmas and without an error (a contract violation): it must not
// be counted as a restart.
var errRestartNotAttempted = errors.New("database: WAL restart was not attempted")

// backoffRestart arms the window before the next restart attempt after a
// refused one, doubling the previous window up to the cap.
func (w *messageWriter) backoffRestart() {
	if w.restartBackoff == 0 {
		w.restartBackoff = restartBackoffMin
	} else if w.restartBackoff < restartBackoffMax {
		w.restartBackoff *= 2
		if w.restartBackoff > restartBackoffMax {
			w.restartBackoff = restartBackoffMax
		}
	}
	w.restartAfter = time.Now().Add(w.restartBackoff)
}

func (w *messageWriter) submit(job *messageJob) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWriterClosed
	}
	w.inflight++
	if !w.busy && len(w.jobs) == 0 {
		// In-line: the lane is idle, so run the job here instead of waking
		// the loop. The busy mark keeps writers that arrive while this one
		// runs on the queue, where the group commit can batch them. nextJob
		// and the shutdown drain wait for this owner's release, including
		// its checkpoint check, before taking another job.
		w.busy = true
		w.mu.Unlock()
		w.runInline(job)
		w.mu.Lock()
		w.inflight--
		w.busy = false
		w.mu.Unlock()
		select {
		case w.wake <- struct{}{}:
		default:
		}
		return nil
	}
	select {
	case w.jobs <- job:
		w.mu.Unlock()
	default:
		// The queue is full: drop the mutex before the blocking send so
		// Close and in-line claims are never held up by backpressure.
		w.mu.Unlock()
		w.jobs <- job
	}
	// Kick the loop; it re-probes the channel rather than consuming the
	// wake, so a job is never taken by the signal itself. The buffered
	// channel coalesces kicks while the loop is already awake.
	select {
	case w.wake <- struct{}{}:
	default:
	}
	w.mu.Lock()
	w.inflight--
	w.mu.Unlock()
	return nil
}

// Close stops the loop after it has drained the queue: every job submitted
// before Close ran to completion (submitted after returns ErrWriterClosed).
// Jobs running in-line on their callers are part of the inflight count, so
// Close waits for them too. Close blocks until the queue is empty and the
// loop has exited. It is idempotent.
func (w *messageWriter) Close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		close(w.stop)
		<-w.done
		if w.telemetry {
			slog.Info("wal restart summary",
				"invocations", w.restartInvocations.Load(),
				"attempts", w.restartAttempts.Load(),
				"completed", w.restarts.Load(),
				"incomplete", w.restartIncomplete.Load(),
				"invocation_errors_including_cleanup", w.restartErrors.Load(),
				"deferred", w.restartDeferred.Load(),
				"stall_ms", w.restartStallNanos.Load()/int64(time.Millisecond),
				"stall_max_ms", w.restartStallMax.Load()/int64(time.Millisecond),
				"passive_ms", w.restartPassiveNanos.Load()/int64(time.Millisecond),
				"restart_ms", w.restartRestartNanos.Load()/int64(time.Millisecond),
				"limit", w.limit)
		}
	})
}

func (w *messageWriter) loop() {
	defer close(w.done)
	for {
		job, stop := w.nextJob()
		if stop {
			w.drain()
			return
		}
		w.runBatch(job)
		// The WAL-growth ceiling is checked with the writer still marked
		// busy, so an in-line claim cannot start a transaction on the lane
		// between the commit and the checkpoint.
		w.walLimit()
		w.mu.Lock()
		w.busy = false
		w.mu.Unlock()
	}
}

// nextJob takes the next queued job, marking the writer busy in the same
// critical section that takes it, or reports the stop signal. Taking the job
// under the mutex closes the window in which an in-line claim could observe
// an idle writer with a job already claimed here (a later write overtaking
// an earlier one). When the queue is empty the loop sleeps on the wake
// channel; the wake is only a kick — the channel itself never carries jobs,
// so no job is consumed by the signal.
func (w *messageWriter) nextJob() (job *messageJob, stop bool) {
	for {
		select {
		case <-w.stop:
			return nil, true
		default:
		}
		w.mu.Lock()
		if w.busy {
			w.mu.Unlock()
			select {
			case <-w.stop:
				return nil, true
			case <-w.wake:
			}
			continue
		}
		select {
		case job := <-w.jobs:
			w.busy = true
			w.mu.Unlock()
			return job, false
		default:
			w.mu.Unlock()
		}
		// Sleep without the mutex until a submit kicks us (or Close stops
		// us); the probe above is authoritative about what to take.
		select {
		case <-w.stop:
			return nil, true
		case <-w.wake:
		}
	}
}

// LaneBlocker holds the write lane busy until the returned release runs:
// message jobs submitted while it is held take the queued batch path instead
// of running in-line, which tests use to pin the queued path's behaviour
// deterministically. The blocker's own transaction commits nothing.
func (d *DB) LaneBlocker() func() {
	gate := make(chan struct{})
	var once sync.Once
	job := &messageJob{
		ctx:  context.Background(),
		run:  func(tx jobTx) (Message, error) { <-gate; return Message{}, nil },
		done: make(chan messageResult, 1),
	}
	go func() {
		if err := d.writer.submit(job); err != nil {
			panic(err)
		}
	}()
	return func() {
		once.Do(func() {
			close(gate)
			<-job.done
		})
	}
}

// runInline executes one job on the caller's goroutine: its own transaction,
// the after-commit statements inside the same transaction, one commit — the
// direct path's shape, without the CAMPFIRE_WRITE_QUEUE=off flag. The result
// is delivered only after the commit, so a caller's response can never
// precede its message's persistence. Cancellation behaves like the direct
// path: a cancelled context fails the transaction before anything runs, and
// the job's statements carry their own context. A panicking job fails this
// job's write (its transaction rolls back) instead of taking the caller
// down, like the batch path.
func (w *messageWriter) runInline(job *messageJob) {
	var result messageResult
	func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("message write panicked", "error", p)
				result = messageResult{err: fmt.Errorf("database: message write panic: %v", p)}
			}
		}()
		tx, err := w.conn.begin(job.ctx)
		if err != nil {
			result = messageResult{err: err}
			return
		}
		defer tx.rollback()
		m, err := job.run(tx)
		if err == nil && job.after != nil {
			err = job.after(job.ctx, tx, m)
		}
		if err != nil {
			result = messageResult{err: err}
			return
		}
		if err := tx.commit(); err != nil {
			result = messageResult{err: err}
			return
		}
		result = messageResult{message: m}
	}()
	// The in-line job's transaction has committed (or rolled back) and the
	// lane connection is free again; the WAL-growth ceiling is checked here
	// too, so a lone poster's writes are bounded like a batch's.
	w.walLimit()
	job.done <- result
}

// drain processes every job still queued; it is the shutdown path and waits
// for submissions that passed the closed check but had not sent yet (their
// count is inflight, and their send lands in the buffered channel). In-line
// jobs running on their callers also keep the count above zero until they
// finish.
func (w *messageWriter) drain() {
	for {
		w.mu.Lock()
		if w.busy {
			w.mu.Unlock()
			runtime.Gosched()
			continue
		}
		select {
		case job := <-w.jobs:
			w.busy = true
			w.mu.Unlock()
			w.runBatch(job)
			w.walLimit()
			w.mu.Lock()
			w.busy = false
			w.mu.Unlock()
		default:
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
// transaction, then runs the batch's after-commit work in the writer-owned
// second phase. Results are delivered only after both phases, so a caller's
// response can never precede its message's persistence or its after-commit
// work. A single-job batch runs directly in the transaction (a failure rolls
// back exactly its own statements, which is all there is); larger batches run
// each job inside its own savepoint, so one failing job rolls back only its
// own statements while the rest of the batch still commits.
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
	var tx laneTx
	func() {
		// A panicking job must not take the writer down with it; its batch is
		// rolled back and every job in it fails, like the legacy path.
		defer func() {
			if p := recover(); p != nil {
				slog.Error("message write batch panicked", "error", p)
				commitErr = fmt.Errorf("database: message write panic: %v", p)
				if tx != nil {
					tx.rollback()
				}
			}
		}()
		var err error
		tx, err = w.conn.begin(context.Background())
		if err != nil {
			commitErr = err
			return
		}
		if len(batch) == 1 {
			job := batch[0]
			if err := job.ctx.Err(); err != nil {
				// Cancelled before execution: nothing ran, nothing persists,
				// exactly like the legacy BeginTx failing on a cancelled ctx.
				tx.rollback()
				results[0] = messageResult{err: err}
			} else if m, err := job.run(tx); err != nil {
				tx.rollback()
				results[0] = messageResult{err: err}
			} else if err := tx.commit(); err != nil {
				results[0] = messageResult{err: err}
			} else {
				results[0] = messageResult{message: m}
			}
			return
		}
		for i, job := range batch {
			if err := job.ctx.Err(); err != nil {
				// Cancelled before execution: nothing ran, nothing persists,
				// exactly like the legacy BeginTx failing on a cancelled ctx.
				results[i] = messageResult{err: err}
				continue
			}
			if err := tx.savepoint(); err != nil {
				results[i] = messageResult{err: err}
				continue
			}
			m, err := job.run(tx)
			if err != nil {
				tx.rollbackTo()
				tx.releaseSavepoint()
				results[i] = messageResult{err: err}
				continue
			}
			if err := tx.releaseSavepoint(); err != nil {
				results[i] = messageResult{err: err}
				continue
			}
			results[i] = messageResult{message: m}
		}
		if err := tx.commit(); err != nil {
			commitErr = err
		}
	}()
	// The second phase runs only when the primary commit landed: a failed
	// commit persisted no row, so there is no committed message whose index
	// row should exist, and every successful job in the batch fails with the
	// commit error (the shape the caller's own after transaction never had the
	// chance to run).
	afterChunksAttempted := 0
	if commitErr == nil {
		afterChunksAttempted = w.runAfter(batch, results)
	}
	// Report the batch before delivering results, so an observer's record is
	// complete by the time every caller has returned (the histogram test reads
	// it after wg.Wait).
	if w.observe != nil {
		w.observe(len(batch), afterChunksAttempted)
	}
	for i, job := range batch {
		if result := results[i]; result.err == nil && commitErr != nil {
			// The commit failed: no statement of the batch persisted, so the
			// job's own savepoint work is gone with it.
			results[i] = messageResult{err: commitErr}
		}
		job.done <- results[i]
	}
}

// runAfter is the writer-owned second phase (ENGINE-45): the after-commit work
// of every job whose primary work committed, run in bounded transactions
// after the primary commit. It returns the number of after-phase chunks
// attempted, including begin/transaction failures (zero without after work), which the batch
// observer reports. The work runs on the writer's goroutine and lane, so a
// later batch cannot overtake an earlier batch's after work; the after
// statements are exactly the job.after surface the caller used to run, so the
// two lanes and the in-line path keep the same SQL in the same order.
func (w *messageWriter) runAfter(batch []*messageJob, results []messageResult) int {
	idxs := make([]int, 0, len(batch))
	for i, job := range batch {
		if job.after != nil && results[i].err == nil {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return 0
	}
	chunksAttempted := 0
	for start := 0; start < len(idxs); start += afterBatchLimit {
		end := start + afterBatchLimit
		if end > len(idxs) {
			end = len(idxs)
		}
		w.runAfterChunk(batch, results, idxs[start:end])
		chunksAttempted++
	}
	return chunksAttempted
}

// runAfterChunk commits one bounded group of after-commit jobs. A single job
// runs directly in the transaction (its failure rolls back exactly its own
// statements); a larger group wraps each job in its own savepoint, so one
// failed after-job rolls back only its own statements while the rest of the
// group still commits, and a failed commit fails every job in the group that
// had not already failed. The statements run on a context derived from
// Background, not the request: the Rust after_commit hooks and Rails'
// after_commit callbacks are not request-cancellable, and the primary commit
// already landed, so a disconnecting client must not abort optional callback
// work. The begin keeps Background (no deadline), matching the shape the
// caller used before the second phase was writer-owned.
func (w *messageWriter) runAfterChunk(batch []*messageJob, results []messageResult, idxs []int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var phaseErr error
	var commitErr error
	var tx laneTx
	func() {
		// A panicking after-job must not take the writer down with it: its
		// chunk is rolled back and every job in it reports the panic.
		defer func() {
			if p := recover(); p != nil {
				slog.Error("message after-commit panicked", "error", p)
				phaseErr = fmt.Errorf("database: message after-commit panic: %v", p)
				if tx != nil {
					tx.rollback()
				}
			}
		}()
		var err error
		tx, err = w.conn.begin(context.Background())
		if err != nil {
			phaseErr = err
			return
		}
		if len(idxs) == 1 {
			i := idxs[0]
			if err := batch[i].after(ctx, tx, results[i].message); err != nil {
				tx.rollback()
				results[i].afterErr = err
				return
			}
			if err := tx.commit(); err != nil {
				results[i].afterErr = err
			}
			return
		}
		for _, i := range idxs {
			if err := tx.savepoint(); err != nil {
				results[i].afterErr = err
				continue
			}
			if err := batch[i].after(ctx, tx, results[i].message); err != nil {
				tx.rollbackTo()
				tx.releaseSavepoint()
				results[i].afterErr = err
				continue
			}
			if err := tx.releaseSavepoint(); err != nil {
				results[i].afterErr = err
				continue
			}
		}
		if err := tx.commit(); err != nil {
			commitErr = err
		}
	}()
	if phaseErr == nil && commitErr == nil {
		return
	}
	for _, i := range idxs {
		if results[i].afterErr != nil {
			continue
		}
		if phaseErr != nil {
			results[i].afterErr = phaseErr
		} else {
			results[i].afterErr = commitErr
		}
	}
}

// checkpointer runs PASSIVE WAL checkpoints on its own connection, off the
// writer. The writer connection has PRAGMA wal_autocheckpoint=0, so no commit
// ever pays the checkpoint's WAL and database fsyncs; the checkpointer pays
// them on a schedule instead, exactly the division of labour the Rust port's
// checkpointer thread implements.
type checkpointer struct {
	conn     *sql.DB
	path     string
	interval time.Duration
	// gate is the checkpoint gate shared with the write lane: SQLite runs one
	// checkpoint at a time, and a checkpoint that arrives while another holds
	// the lock is refused. The checkpointer Locks (its scheduled checkpoint
	// waits for a bounded writer restart); the writer TryLocks.
	gate *checkpointGate
	stop chan struct{}
	done chan struct{}
	once sync.Once
	// telemetry gates the per-checkpoint log line (CAMPFIRE_CHECKPOINT_TELEMETRY);
	// the counters themselves are always kept, and cost one store per checkpoint.
	telemetry bool
	stats     checkpointStats
}

// checkpointStats is the checkpointer's telemetry. Every field is an atomic
// written once per checkpoint, so a reader (the diagnostic accessor or the
// telemetry log line) never takes a lock and the checkpoint path never pays
// for the counters. The numbers answer two questions the fixed interval
// raised: how much of each checkpoint's work is real (frames moved) versus a
// no-op, and how large the WAL grows between checkpoints (frames/backlog) —
// the inputs to the workload-aware policy.
type checkpointStats struct {
	checkpoints  atomic.Int64 // PASSIVE checkpoints run, final drain included
	noops        atomic.Int64 // checkpoints that moved no frames
	moved        atomic.Int64 // frames the latest checkpoint moved
	movedTotal   atomic.Int64 // frames moved across every checkpoint
	walFrames    atomic.Int64 // WAL frame count at the latest checkpoint
	walFramesMax atomic.Int64 // largest WAL frame count seen
	backfilled   atomic.Int64 // backfilled frames at the latest checkpoint
	backlog      atomic.Int64 // frames the latest checkpoint left in the WAL
	backlogMax   atomic.Int64 // largest such backlog
	busy         atomic.Int64 // checkpoints that could not finish (busy != 0)
	durNanos     atomic.Int64 // total checkpoint duration
	durMaxNanos  atomic.Int64 // longest single checkpoint
	lastUnixNano atomic.Int64 // when the latest checkpoint ran
	walBytes     atomic.Int64 // -wal file size at the latest checkpoint
	walBytesMax  atomic.Int64 // largest -wal file size seen
}

// checkpointSnapshot is a plain-value copy of checkpointStats for assertions
// and diagnostics.
type checkpointSnapshot struct {
	Checkpoints  int64
	Noops        int64
	Moved        int64
	MovedTotal   int64
	WALFrames    int64
	WALFramesMax int64
	Backlog      int64
	BacklogMax   int64
	Busy         int64
	DurNanos     int64
	DurMaxNanos  int64
	LastUnixNano int64
	WALBytes     int64
	WALBytesMax  int64
}

func (c *checkpointer) snapshot() checkpointSnapshot {
	return checkpointSnapshot{
		Checkpoints:  c.stats.checkpoints.Load(),
		Noops:        c.stats.noops.Load(),
		Moved:        c.stats.moved.Load(),
		MovedTotal:   c.stats.movedTotal.Load(),
		WALFrames:    c.stats.walFrames.Load(),
		WALFramesMax: c.stats.walFramesMax.Load(),
		Backlog:      c.stats.backlog.Load(),
		BacklogMax:   c.stats.backlogMax.Load(),
		Busy:         c.stats.busy.Load(),
		DurNanos:     c.stats.durNanos.Load(),
		DurMaxNanos:  c.stats.durMaxNanos.Load(),
		LastUnixNano: c.stats.lastUnixNano.Load(),
		WALBytes:     c.stats.walBytes.Load(),
		WALBytesMax:  c.stats.walBytesMax.Load(),
	}
}

// checkpointTelemetry reads CAMPFIRE_CHECKPOINT_TELEMETRY (default off): on
// logs one line per checkpoint and a summary at shutdown, so a workload run
// can be judged from the application log. It changes no behaviour.
func checkpointTelemetry() bool {
	raw := os.Getenv("CAMPFIRE_CHECKPOINT_TELEMETRY")
	if raw == "" {
		return false
	}
	on, valid := parseWriteQueue(raw)
	if !valid {
		slog.Warn("invalid CAMPFIRE_CHECKPOINT_TELEMETRY; keeping telemetry off", "value", raw)
		return false
	}
	return on
}

// updateMax raises *dst to v when v is larger (lock-free max).
func updateMax(dst *atomic.Int64, v int64) {
	for {
		cur := dst.Load()
		if v <= cur || dst.CompareAndSwap(cur, v) {
			return
		}
	}
}

// walFileBytes reports the -wal file's size, the WAL's high-water mark on
// disk (journal_size_limit stays -1, so a checkpoint does not truncate it).
// A missing WAL is zero.
func walFileBytes(path string) int64 {
	if info, err := os.Stat(path + "-wal"); err == nil {
		return info.Size()
	}
	return 0
}

func startCheckpointer(driver, dsn, path string, interval time.Duration, gate *checkpointGate) (*checkpointer, error) {
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	c := &checkpointer{
		conn:      conn,
		path:      path,
		interval:  interval,
		gate:      gate,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		telemetry: checkpointTelemetry(),
	}
	go c.loop()
	return c, nil
}

func (c *checkpointer) loop() {
	defer close(c.done)
	if c.interval <= 0 {
		// The timed fallback is off (CAMPFIRE_CHECKPOINT_MS=0): the write
		// lane's growth ceiling schedules checkpoints until the final drain
		// at Close.
		<-c.stop
		return
	}
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
	// One checkpoint at a time: the gate keeps this from being refused by a
	// write-lane restart (and vice versa). The writer TryLocks, so it never
	// queues here; this Lock waits at most one bounded restart attempt.
	if c.gate != nil {
		c.gate.lock()
		defer c.gate.unlock()
	}
	start := time.Now()
	previous := c.stats.lastUnixNano.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// PASSIVE never blocks: frames a reader is using are skipped and picked up
	// on the next tick. The row reports (busy, log pages, checkpointed pages):
	// log is the WAL's frame count and checked the frames backfilled so far in
	// this WAL generation, so their difference is the backlog a reader is
	// still pinning and the growth since the last checkpoint is the change in
	// checked.
	var busy, log, checked int
	err := c.conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &log, &checked)
	elapsed := time.Since(start)
	now := start.UnixNano()
	previousBackfilled := c.stats.backfilled.Load()
	moved := int64(checked) - previousBackfilled
	if moved < 0 {
		// The WAL restarted (mxFrame and the backfill count dropped), so this
		// checkpoint's work is the whole backfill count.
		moved = int64(checked)
	}
	c.stats.checkpoints.Add(1)
	if moved == 0 {
		c.stats.noops.Add(1)
	}
	c.stats.moved.Store(moved)
	c.stats.movedTotal.Add(moved)
	c.stats.walFrames.Store(int64(log))
	updateMax(&c.stats.walFramesMax, int64(log))
	c.stats.backfilled.Store(int64(checked))
	c.stats.backlog.Store(int64(log - checked))
	updateMax(&c.stats.backlogMax, int64(log-checked))
	if busy != 0 {
		c.stats.busy.Add(1)
	}
	c.stats.durNanos.Add(elapsed.Nanoseconds())
	updateMax(&c.stats.durMaxNanos, elapsed.Nanoseconds())
	c.stats.lastUnixNano.Store(now)
	wal := walFileBytes(c.path)
	c.stats.walBytes.Store(wal)
	updateMax(&c.stats.walBytesMax, wal)
	if err != nil {
		slog.Warn("WAL checkpoint failed", "error", err)
	}
	if c.telemetry {
		since := int64(0)
		if previous != 0 {
			since = (now - previous) / int64(time.Millisecond)
		}
		slog.Info("wal checkpoint",
			"frames", log, "moved", moved, "backlog", log-checked, "busy", busy,
			"dur_us", elapsed.Microseconds(), "since_ms", since, "wal_kb", wal/1024)
	}
}

func (c *checkpointer) Close() {
	c.once.Do(func() {
		close(c.stop)
		<-c.done
		c.passive() // one final checkpoint after the writer drained
		if c.telemetry {
			s := c.snapshot()
			slog.Info("wal checkpoint summary",
				"checkpoints", s.Checkpoints, "noops", s.Noops,
				"frames_moved", s.MovedTotal, "wal_frames_max", s.WALFramesMax,
				"backlog_max", s.BacklogMax, "busy", s.Busy,
				"dur_total_ms", s.DurNanos/int64(time.Millisecond),
				"dur_max_ms", s.DurMaxNanos/int64(time.Millisecond),
				"wal_max_kb", s.WALBytesMax/1024)
		}
		c.conn.Close()
	})
}
