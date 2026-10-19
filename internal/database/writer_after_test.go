package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The ENGINE-45 after-phase contract (Lever 1): the queued path's after-commit
// work (search index row + unread bump) is the writer's second phase, batched
// into bounded transactions after the primary commit, so a batch of B
// acknowledged messages costs one primary commit plus the after phase's
// transactions instead of the B+1 the per-caller shape paid. These tests pin
// the commit and savepoint budget exactly, plus the failure/cancellation
// isolation of the second phase.

// countingLane wraps the write lane and counts transaction control, so the
// batch's commit and savepoint budget is observable without a driver hook.
type countingLane struct {
	inner                             laneConn
	mu                                sync.Mutex
	begins, commits, rollbacks        int
	savepoints, rollbackTos, releases int
}

func (c *countingLane) begin(ctx context.Context) (laneTx, error) {
	tx, err := c.inner.begin(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.begins++
	c.mu.Unlock()
	return &countingTx{laneTx: tx, c: c}, nil
}

func (c *countingLane) close() error { return c.inner.close() }

func (c *countingLane) walFrames() int64 { return c.inner.walFrames() }

func (c *countingLane) backfillRestart() (checkpointOutcome, error) {
	return c.inner.backfillRestart()
}

func (c *countingLane) counts() (begin, commit, savepoint, rollbackTo int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.begins, c.commits, c.savepoints, c.rollbackTos
}

type countingTx struct {
	laneTx
	c *countingLane
}

func (t *countingTx) commit() error {
	t.c.mu.Lock()
	t.c.commits++
	t.c.mu.Unlock()
	return t.laneTx.commit()
}

func (t *countingTx) rollback() error {
	t.c.mu.Lock()
	t.c.rollbacks++
	t.c.mu.Unlock()
	return t.laneTx.rollback()
}

func (t *countingTx) savepoint() error {
	t.c.mu.Lock()
	t.c.savepoints++
	t.c.mu.Unlock()
	return t.laneTx.savepoint()
}

func (t *countingTx) rollbackTo() error {
	t.c.mu.Lock()
	t.c.rollbackTos++
	t.c.mu.Unlock()
	return t.laneTx.rollbackTo()
}

func (t *countingTx) releaseSavepoint() error {
	t.c.mu.Lock()
	t.c.releases++
	t.c.mu.Unlock()
	return t.laneTx.releaseSavepoint()
}

// afterScratch prepares a database with two scratch tables the synthetic jobs
// write through the lane, so every commit in the batch is real.
func afterScratch(t *testing.T) *DB {
	t.Helper()
	d := testDB(t)
	for _, ddl := range []string{
		"CREATE TABLE scratch(id INTEGER PRIMARY KEY)",
		"CREATE TABLE after_scratch(id INTEGER PRIMARY KEY)",
	} {
		if _, err := d.Write.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// batchWriter builds a messageWriter over a counting wrapper of d's lane,
// without starting the loop: the test enqueues the jobs and calls runBatch
// itself, so the batch composition is exact.
func batchWriter(d *DB) (*messageWriter, *countingLane) {
	c := &countingLane{inner: d.lane}
	return &messageWriter{
		conn: c,
		jobs: make(chan *messageJob, writeQueueCapacity),
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}, c
}

// syntheticJob is one queued message job: run inserts a row, after inserts the
// after row, mirroring the create path's two phases. cancelBefore marks the
// job cancelled before execution; afterErrAt makes that job's after fail.
func syntheticJob(id int, cancelBefore bool, afterErrAt int) *messageJob {
	ctx := context.Background()
	if cancelBefore {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		ctx = cancelled
	}
	return &messageJob{
		ctx: ctx,
		run: func(tx jobTx) (Message, error) {
			if _, err := tx.ExecContext(ctx, "INSERT INTO scratch(id) VALUES (?)", id); err != nil {
				return Message{}, err
			}
			return Message{ID: int64(id + 1)}, nil
		},
		after: func(afterCtx context.Context, tx jobTx, m Message) error {
			if afterErrAt == id {
				return errors.New("after boom")
			}
			if _, err := tx.ExecContext(afterCtx, "INSERT INTO after_scratch(id) VALUES (?)", m.ID); err != nil {
				return err
			}
			return nil
		},
		done: make(chan messageResult, 1),
	}
}

// runSyntheticBatch enqueues n jobs (jobs[0] is the head) and runs one batch,
// returning the delivered results.
func runSyntheticBatch(w *messageWriter, n, cancelBefore, afterErrAt int) []messageResult {
	jobs := make([]*messageJob, n)
	for i := 0; i < n; i++ {
		jobs[i] = syntheticJob(i, cancelBefore == i, afterErrAt)
		if i > 0 {
			w.jobs <- jobs[i]
		}
	}
	w.runBatch(jobs[0])
	results := make([]messageResult, n)
	for i, job := range jobs {
		results[i] = <-job.done
	}
	return results
}

func afterScratchRows(t *testing.T, d *DB) int {
	t.Helper()
	n, err := d.readInt(context.Background(), "SELECT count(*) FROM after_scratch")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestWriteQueueAfterPhaseCommitBudget pins the commits and savepoints per
// acknowledged message: a batch of B messages costs one primary commit and
// ceil(B/afterBatchLimit) after-phase commits (one when B fits the bound), so
// the per-message commit count falls from the B+1 shape to (1+chunks)/B. The
// savepoint budget is one per job in the primary multi-job batch and one per
// job in a multi-job after chunk; single-job phases use no savepoint.
func TestWriteQueueAfterPhaseCommitBudget(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		d := afterScratch(t)
		ctx := context.Background()

		cases := []struct {
			name           string
			batch          int
			wantCommits    int
			wantSavepoints int
		}{
			{"single", 1, 2, 0},
			{"small", 5, 2, 10},
			{"bound", afterBatchLimit, 2, 2 * afterBatchLimit},
			// The primary batch still savepoints every job; the after phase
			// splits into a full multi-job chunk and a single-job chunk, and
			// the single-job chunk uses no savepoint.
			{"over-bound", afterBatchLimit + 1, 3, (afterBatchLimit + 1) + afterBatchLimit},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				w, c := batchWriter(d)
				results := runSyntheticBatch(w, tc.batch, -1, -1)
				begin, commit, savepoint, _ := c.counts()
				for i, r := range results {
					if r.err != nil || r.afterErr != nil {
						t.Fatalf("job %d: err=%v afterErr=%v", i, r.err, r.afterErr)
					}
				}
				// The primary phase is one begin/commit; the after phase adds
				// one begin/commit per chunk.
				if wantBegin := tc.wantCommits; begin != wantBegin {
					t.Errorf("begins = %d, want %d", begin, wantBegin)
				}
				if commit != tc.wantCommits {
					t.Errorf("commits = %d, want %d (per message %.3f)", commit, tc.wantCommits, float64(commit)/float64(tc.batch))
				}
				if savepoint != tc.wantSavepoints {
					t.Errorf("savepoints = %d, want %d", savepoint, tc.wantSavepoints)
				}
				if got := afterScratchRows(t, d); got != tc.batch {
					t.Fatalf("after rows = %d, want %d", got, tc.batch)
				}
				if _, err := d.Write.ExecContext(ctx, "DELETE FROM scratch"); err != nil {
					t.Fatal(err)
				}
				if _, err := d.Write.ExecContext(ctx, "DELETE FROM after_scratch"); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}

// TestWriteQueueAfterPhaseIsolation pins the second phase's per-job isolation:
// one failed after-job rolls back exactly its own savepoint while the rest of
// the chunk commits, and the failure is observable to that job's caller only.
func TestWriteQueueAfterPhaseIsolation(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		d := afterScratch(t)
		w, c := batchWriter(d)
		results := runSyntheticBatch(w, 5, -1, 2)
		for i, r := range results {
			switch {
			case i == 2:
				if r.afterErr == nil {
					t.Fatalf("job %d: after error not reported", i)
				}
			default:
				if r.err != nil || r.afterErr != nil {
					t.Fatalf("job %d: err=%v afterErr=%v", i, r.err, r.afterErr)
				}
			}
		}
		if got := afterScratchRows(t, d); got != 4 {
			t.Fatalf("after rows = %d, want 4 (the failed job's savepoint rolled back)", got)
		}
		// The primary batch committed every row: the after failure must not
		// touch the message rows.
		if n, err := d.readInt(context.Background(), "SELECT count(*) FROM scratch"); err != nil || n != 5 {
			t.Fatalf("scratch rows = %d %v, want 5", n, err)
		}
		// One commit for the primary batch, one for the after chunk; the
		// failed job's savepoint rolled back without failing the chunk.
		_, commit, savepoint, rollbackTo := c.counts()
		if commit != 2 {
			t.Errorf("commits = %d, want 2", commit)
		}
		if savepoint != 10 {
			t.Errorf("savepoints = %d, want 10 (5 primary + 5 after)", savepoint)
		}
		if rollbackTo != 1 {
			t.Errorf("rollbackTo = %d, want 1", rollbackTo)
		}
	})
}

// TestWriteQueueAfterPhaseCancellation pins that a job cancelled before
// execution persists nothing and runs no after work, while the rest of its
// batch commits both phases — the shutdown/cancellation survival contract.
func TestWriteQueueAfterPhaseCancellation(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		d := afterScratch(t)
		w, _ := batchWriter(d)
		results := runSyntheticBatch(w, 4, 1, -1)
		for i, r := range results {
			if i == 1 {
				if !errors.Is(r.err, context.Canceled) {
					t.Fatalf("cancelled job: err = %v, want context.Canceled", r.err)
				}
				continue
			}
			if r.err != nil || r.afterErr != nil {
				t.Fatalf("job %d: err=%v afterErr=%v", i, r.err, r.afterErr)
			}
		}
		if n, err := d.readInt(context.Background(), "SELECT count(*) FROM scratch"); err != nil || n != 3 {
			t.Fatalf("scratch rows = %d %v, want 3", n, err)
		}
		if got := afterScratchRows(t, d); got != 3 {
			t.Fatalf("after rows = %d, want 3", got)
		}
	})
}

// observedSeededWriter installs the batch observer and then seeds a writer
// test database, so the writer captures the observer at open. The returned
// slices are guarded by the returned mutex.
func observedSeededWriter(t *testing.T) (d *DB, owner, room int64, sizes, chunks *[]int, mu *sync.Mutex) {
	t.Helper()
	mu = &sync.Mutex{}
	ps, cs := &[]int{}, &[]int{}
	old := writerBatchObserver
	writerBatchObserver = func(p, a int) {
		mu.Lock()
		*ps = append(*ps, p)
		*cs = append(*cs, a)
		mu.Unlock()
	}
	t.Cleanup(func() { writerBatchObserver = old })
	t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	d, owner, _, _, room = seededWriterTest(t)
	return d, owner, room, ps, cs, mu
}

// runPostBurst drives `posters` concurrent CreateMessage calls behind a held
// lane, so every post queues and the writer's group commit forms the batches.
func runPostBurst(t *testing.T, d *DB, owner, room int64, posters int) {
	t.Helper()
	ctx := context.Background()
	release := d.LaneBlocker()
	defer release()
	for {
		d.writer.mu.Lock()
		busy := d.writer.busy
		d.writer.mu.Unlock()
		if busy {
			break
		}
		time.Sleep(time.Millisecond)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, posters)
	for i := 0; i < posters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = d.CreateMessage(ctx, owner, room, "", "<p>burst</p>", "burst")
		}(i)
	}
	close(start)
	// Every poster is queued behind the held lane before the gate opens.
	time.Sleep(100 * time.Millisecond)
	release()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("poster %d: %v", i, err)
		}
	}
}

// TestWriteQueueBatchHistogram drives concurrent posts behind a held lane and
// records the writer's own batch sizes and after-phase commit counts. Every
// acknowledged message appears in exactly one batch; message derived state
// now commits in the primary transaction, with no after phase; and a multi-job batch (group
// commit) is observed, which is the whole point of the queue.
func TestWriteQueueBatchHistogram(t *testing.T) {
	d, owner, room, ps, cs, mu := observedSeededWriter(t)
	ctx := context.Background()
	const posters = 32
	runPostBurst(t, d, owner, room, posters)

	mu.Lock()
	sizes := append([]int(nil), (*ps)...)
	chunks := append([]int(nil), (*cs)...)
	mu.Unlock()
	if len(sizes) != len(chunks) {
		t.Fatalf("observer recorded %d primary batches, %d after counts", len(sizes), len(chunks))
	}
	total, commits, maxBatch := 0, 0, 0
	for i, size := range sizes {
		total += size
		commits += 1 + chunks[i]
		if size > maxBatch {
			maxBatch = size
		}
		if want := 0; chunks[i] != want {
			t.Errorf("batch %d size %d: after transactions = %d, want %d", i, size, chunks[i], want)
		}
	}
	if total != posters {
		t.Fatalf("batched messages = %d, want %d", total, posters)
	}
	if maxBatch < 2 {
		t.Fatalf("no group commit observed: batch sizes %v", sizes)
	}
	if commits >= posters+1 {
		t.Fatalf("commits = %d for %d messages; want fewer than the B+1 shape", commits, posters)
	}
	t.Logf("batch sizes %v; commits %d for %d messages (%.3f/message)", sizes, commits, posters, float64(commits)/float64(posters))

	// The batch sizes are the histogram; every message persisted with its
	// search row exactly once.
	if n, err := d.readInt(ctx, "SELECT count(*) FROM messages WHERE room_id=?", room); err != nil || n != posters {
		t.Fatalf("messages = %d %v, want %d", n, err, posters)
	}
	if n, err := d.readInt(ctx, "SELECT count(*) FROM message_search_index"); err != nil || n != posters {
		t.Fatalf("search rows = %d %v, want %d", n, err, posters)
	}
}

// TestWriteQueueDrainsAfterPhaseOnClose pins shutdown with the second phase:
// posts queued behind a held lane are drained by Close, and every message that
// committed also got its search row before the lane closed — the writer's
// after phase runs inside the drain, so a committed write never fails its
// after work on a torn-down lane.
func TestWriteQueueDrainsAfterPhaseOnClose(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.sqlite3")
		d, err := Open(path, 4)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		owner, err := d.Setup(ctx, "Owner", "owner@test", "digest")
		if err != nil {
			t.Fatal(err)
		}
		room, err := d.CreateRoom(ctx, owner.ID, "Rooms::Open", "Drain", nil)
		if err != nil {
			t.Fatal(err)
		}
		release := d.LaneBlocker()
		for {
			d.writer.mu.Lock()
			busy := d.writer.busy
			d.writer.mu.Unlock()
			if busy {
				break
			}
			time.Sleep(time.Millisecond)
		}
		const posters = 12
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, posters)
		for i := 0; i < posters; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = d.CreateMessage(ctx, owner.ID, room.ID, fmt.Sprintf("drain-%d", i), "<p>drain</p>", fmt.Sprintf("drain %d", i))
			}(i)
		}
		close(start)
		time.Sleep(50 * time.Millisecond) // every poster queued behind the blocker
		go release()
		d.Close()
		wg.Wait()
		committed := 0
		for i, err := range errs {
			if err == nil {
				committed++
				continue
			}
			if !errors.Is(err, ErrWriterClosed) {
				t.Fatalf("poster %d: %v", i, err)
			}
		}
		if committed == 0 {
			t.Fatal("no queued post drained before close")
		}
		check, err := Open(path, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer check.Close()
		messages, err := check.readInt(ctx, "SELECT count(*) FROM messages WHERE room_id=?", room.ID)
		if err != nil {
			t.Fatal(err)
		}
		search, err := check.readInt(ctx, "SELECT count(*) FROM message_search_index")
		if err != nil {
			t.Fatal(err)
		}
		if messages != committed || search != committed {
			t.Fatalf("drained %d committed: messages=%d search=%d, want both %d", committed, messages, search, committed)
		}
	})
}

// under a larger concurrent burst. The same batch histogram yields both the
// old per-caller shape (one after commit per message: batches + messages) and
// the writer-owned second phase (batches + after chunks), so the change's
// commit cut is measured from one run rather than compared across binaries.
// Skipped unless CAMPFIRE_MEASURE_COMMITS is set; the batch distribution is
// scheduling-dependent, the commit identity is not.
func TestWriteQueueCommitMeasurement(t *testing.T) {
	if os.Getenv("CAMPFIRE_MEASURE_COMMITS") == "" {
		t.Skip("set CAMPFIRE_MEASURE_COMMITS=1 to run the commit measurement")
	}
	posters := 2000
	if raw := os.Getenv("CAMPFIRE_MEASURE_POSTERS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			t.Fatalf("bad CAMPFIRE_MEASURE_POSTERS %q", raw)
		}
		posters = n
	}
	d, owner, room, ps, cs, mu := observedSeededWriter(t)
	runPostBurst(t, d, owner, room, posters)

	mu.Lock()
	sizes := append([]int(nil), (*ps)...)
	chunks := append([]int(nil), (*cs)...)
	mu.Unlock()
	afterTotal, beforeTotal := 0, 0
	for i, size := range sizes {
		afterTotal += 1 + chunks[i]
		beforeTotal += 1 + size
	}
	t.Logf("messages=%d batches=%d batch_sizes=%v", posters, len(sizes), sizes)
	t.Logf("commits/message: per-caller(before)=%.4f writer-second-phase(after)=%.4f", float64(beforeTotal)/float64(posters), float64(afterTotal)/float64(posters))
	t.Logf("total commits: before=%d after=%d", beforeTotal, afterTotal)
}
