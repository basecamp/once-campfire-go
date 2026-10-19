package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/once-campfire-go/internal/fastdb/write"
)

// walFileSize reports the database's -wal file size (0 when the file is
// absent, as it is after the last connection closes).
func walFileSize(path string) int64 {
	if info, err := os.Stat(path + "-wal"); err == nil {
		return info.Size()
	}
	return 0
}

// seedPoster opens a database at path and returns it with one user id and one
// room id to post to.
func seedPoster(t *testing.T, path string, readers int) (*DB, int64, int64) {
	t.Helper()
	d, err := Open(path, readers)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user, err := d.Setup(ctx, "Owner", "owner@test", "digest")
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	rooms, err := d.Rooms(ctx, user.ID)
	if err != nil || len(rooms) == 0 {
		d.Close()
		t.Fatalf("rooms: %v %v", rooms, err)
	}
	return d, user.ID, rooms[0].ID
}

// TestWALStaysBoundedUnderSustainedWrites is the Lever 4 contract: with the
// growth ceiling on (the default), a sustained post workload must keep the WAL
// file bounded, and the run must still end with the same durable state — the
// writer drains, the final checkpoint runs, the WAL is removed, and every
// committed message survives a reopen. Before the ceiling, the same workload
// reached thousands of megabytes (readers starve SQLite's own WAL restart).
func TestWALStaysBoundedUnderSustainedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.sqlite3")
	d, user, room := seedPoster(t, path, 4)
	ctx := context.Background()
	limit := d.writer.limit
	if limit <= 0 {
		t.Fatalf("growth ceiling = %d, want the default on", limit)
	}
	frame := pragmaInt(t, d.Write, "PRAGMA page_size") + 24
	const posts = 4000
	var maxWAL int64
	for i := 0; i < posts; i++ {
		if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("w-%d", i), "<p>bounded</p>", "bounded"); err != nil {
			t.Fatal(err)
		}
		if i%50 == 0 {
			if size := walFileSize(path); size > maxWAL {
				maxWAL = size
			}
		}
	}
	if size := walFileSize(path); size > maxWAL {
		maxWAL = size
	}
	if restarts := d.writer.restarts.Load(); restarts == 0 {
		t.Fatal("no WAL restarts under sustained writes; the ceiling never fired")
	}
	// The WAL may overshoot the ceiling by the frames written between checks
	// and by its high-water mark, but it must stay near the ceiling rather
	// than grow without bound.
	if bound := (limit + 4000) * frame; maxWAL > bound {
		t.Fatalf("WAL reached %d bytes, want under %d (ceiling %d frames)", maxWAL, bound, limit)
	}

	// Final drain: Close drains the writer, runs the last PASSIVE checkpoint
	// and removes the WAL with the last connection.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if size := walFileSize(path); size != 0 {
		t.Fatalf("WAL remains after Close: %d bytes", size)
	}
	reopened, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.Read.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != posts {
		t.Fatalf("messages after reopen = %d, want %d", count, posts)
	}
}

// TestWALGrowthCeilingRollbackSwitch pins the rollback switch: with
// CAMPFIRE_CHECKPOINT_PAGES=0 the writer performs no restarts and the WAL is
// free to pass the default ceiling, which is exactly the pre-change policy.
func TestWALGrowthCeilingRollbackSwitch(t *testing.T) {
	t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "0")
	path := filepath.Join(t.TempDir(), "wal.sqlite3")
	d, user, room := seedPoster(t, path, 4)
	defer d.Close()
	ctx := context.Background()
	if d.writer.limit != 0 {
		t.Fatalf("growth ceiling = %d, want 0 with the switch off", d.writer.limit)
	}
	frame := pragmaInt(t, d.Write, "PRAGMA page_size") + 24
	for i := 0; i < 1500; i++ {
		if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("u-%d", i), "<p>unbounded</p>", "unbounded"); err != nil {
			t.Fatal(err)
		}
	}
	if restarts := d.writer.restarts.Load(); restarts != 0 {
		t.Fatalf("restarts = %d with the ceiling off, want 0", restarts)
	}
	if size := walFileSize(path); size <= 10000*frame {
		t.Fatalf("WAL = %d bytes, want past the default ceiling %d with the limit off", size, 10000*frame)
	}
}

// TestCheckpointTelemetryCounters pins the telemetry: one PASSIVE checkpoint
// records its duration, the frames it moved, the WAL size and the backlog, and
// the counters move only when a checkpoint runs.
func TestCheckpointTelemetryCounters(t *testing.T) {
	t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000") // no timed ticks: passive runs explicitly
	path := filepath.Join(t.TempDir(), "wal.sqlite3")
	d, user, room := seedPoster(t, path, 4)
	defer d.Close()
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("t-%d", i), "<p>telemetry</p>", "telemetry"); err != nil {
			t.Fatal(err)
		}
	}
	before := d.checkpoints.snapshot()
	d.checkpoints.passive()
	after := d.checkpoints.snapshot()
	if after.Checkpoints != before.Checkpoints+1 {
		t.Fatalf("checkpoints = %d, want %d", after.Checkpoints, before.Checkpoints+1)
	}
	if after.MovedTotal <= before.MovedTotal {
		t.Fatalf("frames moved did not grow: %d then %d", before.MovedTotal, after.MovedTotal)
	}
	if after.Moved <= 0 {
		t.Fatalf("latest checkpoint moved %d frames, want > 0", after.Moved)
	}
	if after.LastUnixNano == 0 {
		t.Fatal("no checkpoint timestamp recorded")
	}
	if after.WALBytes == 0 {
		t.Fatal("no WAL size recorded")
	}
	if after.DurNanos <= 0 {
		t.Fatal("no checkpoint duration recorded")
	}
	// A second checkpoint with nothing new to move is the no-op the fixed
	// interval pays for; the counter must see it.
	d.checkpoints.passive()
	noop := d.checkpoints.snapshot()
	if noop.Noops <= after.Noops {
		t.Fatalf("no-op checkpoints = %d, want more than %d", noop.Noops, after.Noops)
	}
}

// TestWALRestartBoundedUnderPinnedReader is the regression for the
// refused-restart HIGH: with a read transaction pinning a WAL read slot, the
// writer's RESTART is refused (reported in the pragma's result row). Before the
// fix the writer counted it as a completed restart and waited the connection's
// full 5000 ms busy timeout on every attempt — 60 posts took 2 m 50 s, worst
// post 5.013 s, with 35 "successful" restarts that never reset the WAL. After
// the fix the attempt is bounded and reported incomplete, and the writer backs
// off instead of stalling again.
func TestWALRestartBoundedUnderPinnedReader(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "200")
		t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000") // no background ticks
		path := filepath.Join(t.TempDir(), "pinned.sqlite3")
		d, user, room := seedPoster(t, path, 4)
		defer d.Close()
		ctx := context.Background()
		post := func(i int) time.Duration {
			t.Helper()
			start := time.Now()
			if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("p-%d", i), "<p>p</p>", "p"); err != nil {
				t.Fatal(err)
			}
			return time.Since(start)
		}
		for i := 0; i < 20; i++ {
			post(i)
		}
		reader, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000&_journal_mode=WAL")
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		reader.SetMaxOpenConns(1)
		tx, err := reader.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var pinned int
		if err := tx.QueryRow("SELECT count(*) FROM messages").Scan(&pinned); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		var worst time.Duration
		for i := 20; i < 420; i++ {
			if d := post(i); d > worst {
				worst = d
			}
		}
		pinnedFor := time.Since(start)
		if d.writer.restartIncomplete.Load() == 0 {
			t.Fatalf("no refused restart recorded with a reader pinned (attempts=%d completed=%d deferred=%d)",
				d.writer.restartAttempts.Load(), d.writer.restarts.Load(), d.writer.restartDeferred.Load())
		}
		// Observed-value regression guard, not a guarantee: the busy-wait
		// setting (write.CheckpointBusyMS) bounds only SQLite's busy-handler
		// retry, and the pre-fix code spent the full 5000 ms busy timeout on
		// every attempt (worst post 5.004 s). The guard is deliberately loose.
		if worst > time.Second {
			t.Fatalf("observed worst post %v with a pinned reader; the pre-fix code took ~5s per attempt (busy-wait setting %d ms)",
				worst, write.CheckpointBusyMS)
		}
		if stall := time.Duration(d.writer.restartStallMax.Load()); stall > time.Second {
			t.Fatalf("observed worst restart invocation %v, want under the 1s regression guard", stall)
		}
		if pinnedFor > 10*time.Second {
			t.Fatalf("400 posts with a pinned reader took %v, want the writer not to stall repeatedly", pinnedFor)
		}
		// The reader holds its slot: a direct attempt is still refused, and it
		// is reported as refused rather than as a restart.
		outcome, err := d.lane.backfillRestart()
		if err != nil {
			t.Fatal(err)
		}
		if !outcome.attempted || outcome.completed {
			t.Fatalf("attempt with the reader still pinned = %+v, want attempted and refused", outcome)
		}

		// Releasing the reader lets the next attempt complete and reset the WAL.
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		outcome, err = d.lane.backfillRestart()
		if err != nil {
			t.Fatal(err)
		}
		if !outcome.completed {
			t.Fatalf("attempt after the reader released = %+v, want completed", outcome)
		}
		// The refused attempts armed the writer's backoff; wait it out (it is
		// bounded by restartBackoffMax) so the next drains are allowed to try.
		if wait := time.Until(d.writer.restartAfter); wait > 0 {
			time.Sleep(wait)
		}
		completedBefore := d.writer.restarts.Load()
		for i := 420; i < 820; i++ {
			post(i)
		}
		if d.writer.restarts.Load() <= completedBefore {
			t.Fatalf("no restart completed after the reader released (restarts=%d incomplete=%d attempts=%d invocations=%d)",
				d.writer.restarts.Load(), d.writer.restartIncomplete.Load(), d.writer.restartAttempts.Load(), d.writer.restartInvocations.Load())
		}
	})
}

// TestWALRestartDefersToCheckpointerGate pins the coordination requirement:
// while the background checkpointer holds the checkpoint gate, the writer must
// not run a restart (SQLite allows one checkpoint at a time, and a restart that
// arrives while another holds the lock is refused) — it defers, without
// stalling, and runs on a later drain once the gate is free.
func TestWALRestartDefersToCheckpointerGate(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "200")
		t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
		path := filepath.Join(t.TempDir(), "gate.sqlite3")
		d, user, room := seedPoster(t, path, 4)
		defer d.Close()
		ctx := context.Background()
		post := func(i int) time.Duration {
			t.Helper()
			start := time.Now()
			if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("g-%d", i), "<p>g</p>", "g"); err != nil {
				t.Fatal(err)
			}
			return time.Since(start)
		}
		if d.writer.gate == nil || d.checkpoints.gate != d.writer.gate {
			t.Fatal("writer and checkpointer must share one checkpoint gate")
		}
		// Hold the gate as a running checkpoint would. Unlock before any
		// assertion that can fail the test: DB.Close runs the checkpointer's
		// final checkpoint, which takes the same gate.
		d.writer.gate.lock()
		var worst time.Duration
		for i := 0; i < 400; i++ {
			if d := post(i); d > worst {
				worst = d
			}
		}
		invocations := d.writer.restartInvocations.Load()
		deferred := d.writer.restartDeferred.Load()
		d.writer.gate.unlock()
		if invocations != 0 {
			t.Fatalf("writer tried %d restarts while the gate was held", invocations)
		}
		if deferred == 0 {
			t.Fatal("no deferral recorded while the gate was held")
		}
		if worst > 500*time.Millisecond {
			t.Fatalf("writer stalled %v while the gate was held; it must defer, not wait", worst)
		}
		for i := 400; i < 800; i++ {
			post(i)
		}
		if d.writer.restartAttempts.Load() == 0 {
			t.Fatal("no restart attempt ran after the gate released")
		}
		if d.writer.restarts.Load() == 0 {
			t.Fatalf("no completed restart after the gate released (attempts=%d incomplete=%d invocations=%d)",
				d.writer.restartAttempts.Load(), d.writer.restartIncomplete.Load(), d.writer.restartInvocations.Load())
		}
	})
}

// TestWALThresholdRecoversAfterLargeBodies pins the threshold's overshoot
// shape: a single large message writes many WAL frames in one transaction, so
// the check that runs after that drain can see the WAL past the threshold.
// There is no backpressure — the write is never refused or delayed to stay
// under the threshold — and the restart on the next drain brings the WAL back,
// so the overshoot is transient rather than cumulative.
func TestWALThresholdRecoversAfterLargeBodies(t *testing.T) {
	runBothLanes(t, func(t *testing.T) {
		t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "200")
		t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
		path := filepath.Join(t.TempDir(), "large.sqlite3")
		d, user, room := seedPoster(t, path, 4)
		defer d.Close()
		ctx := context.Background()
		limit := d.writer.limit
		if limit <= 0 {
			t.Fatalf("threshold = %d, want the default on", limit)
		}
		// Each body is large enough that one post's transaction writes more
		// frames than the threshold allows.
		body := "<p>" + strings.Repeat("x", 256*1024) + "</p>"
		var worst time.Duration
		for i := 0; i < 3; i++ {
			// The database/sql lane's frame probe is throttled to
			// checkpointProbe, so space the posts past it; otherwise its
			// cached count would still be below the threshold for the whole
			// burst and the drain would never check.
			time.Sleep(checkpointProbe)
			start := time.Now()
			if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("big-%d", i), body, "big body"); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > worst {
				worst = d
			}
		}
		if worst > 2*time.Second {
			t.Fatalf("large post took %v; the threshold must not add backpressure", worst)
		}
		if incomplete := d.writer.restartIncomplete.Load(); incomplete != 0 {
			t.Fatalf("large bodies caused %d refused restarts", incomplete)
		}
		if d.writer.restarts.Load() == 0 {
			t.Fatal("no restart after large bodies; the overshoot did not recover")
		}
		// The WAL stays near the threshold rather than at the hundreds of
		// megabytes a large body would leave without a restart.
		if size := walFileSize(path); size > 32<<20 {
			t.Fatalf("WAL = %d bytes after large bodies, want bounded near the threshold", size)
		}
	})
}

// TestSQLLaneCheckpointTimeoutNotLeakedToBorrowers is the regression for the
// pool-borrowing HIGH/MEDIUM: the checkpoint's temporary busy timeout is a
// property of the connection, and SetMaxOpenConns(1) serializes individual
// operations without reserving the connection across them. The lane now
// reserves one *sql.Conn for the whole sequence, so an unrelated borrower (a
// session or account write, a queued after-commit statement) can never observe
// the checkpoint's 250 ms timeout. The borrower loop runs across a refused
// attempt (a pinned reader), a completed attempt, and a failed restore; it
// must only ever read the configured timeout.
func TestSQLLaneCheckpointTimeoutNotLeakedToBorrowers(t *testing.T) {
	t.Setenv("CAMPFIRE_FASTDB_WRITE", "off")
	t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "200")
	t.Setenv("CAMPFIRE_CHECKPOINT_MS", "3600000")
	path := filepath.Join(t.TempDir(), "borrow.sqlite3")
	d, user, room := seedPoster(t, path, 4)
	defer d.Close()
	lane, ok := d.lane.(*sqlLaneConn)
	if !ok {
		t.Fatalf("lane is %T, want the database/sql lane", d.lane)
	}
	ctx := context.Background()
	post := func(i int) {
		t.Helper()
		if _, err := d.CreateMessage(ctx, user, room, fmt.Sprintf("b-%d", i), "<p>b</p>", "b"); err != nil {
			t.Fatal(err)
		}
	}

	// The borrower loop: every read must see the configured timeout, never the
	// checkpoint's.
	var mu sync.Mutex
	var leaked []int64
	seen := 0
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var value int64
			if err := d.Write.QueryRow("PRAGMA busy_timeout").Scan(&value); err != nil {
				continue
			}
			mu.Lock()
			seen++
			if value != write.BusyTimeoutMS {
				leaked = append(leaked, value)
			}
			mu.Unlock()
		}
	}()
	check := func(phase string) {
		t.Helper()
		mu.Lock()
		got, n := append([]int64(nil), leaked...), seen
		mu.Unlock()
		if len(got) > 0 {
			t.Fatalf("%s: borrower observed busy_timeout %v (want only %d) over %d reads", phase, got, write.BusyTimeoutMS, n)
		}
		if n == 0 {
			t.Fatalf("%s: borrower never read the timeout", phase)
		}
	}

	// Refused attempt: a read transaction pins a WAL read slot, so the RESTART
	// runs its whole busy wait with the temporary timeout set.
	for i := 0; i < 20; i++ {
		post(i)
	}
	reader, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var pinned int
	if err := tx.QueryRow("SELECT count(*) FROM messages").Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	outcome, err := d.lane.backfillRestart()
	if err != nil {
		t.Fatal(err)
	}
	if outcome.completed {
		t.Fatalf("attempt with a reader pinned = %+v, want refused", outcome)
	}
	check("refused attempt")

	// Completed attempt: the reader is gone.
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	outcome, err = d.lane.backfillRestart()
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.completed {
		t.Fatalf("attempt after the reader released = %+v, want completed", outcome)
	}
	check("completed attempt")

	// Failed restore: the connection must be discarded, not returned with the
	// checkpoint's timeout still set, so the borrower still reads the
	// configured value.
	lane.restoreStmt = "SELECT * FROM sql_checkpoint_no_such_table"
	_, err = d.lane.backfillRestart()
	lane.restoreStmt = ""
	if err == nil {
		t.Fatal("restore failure was not reported")
	}
	var value int64
	if err := d.Write.QueryRow("PRAGMA busy_timeout").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != write.BusyTimeoutMS {
		t.Fatalf("busy_timeout after a failed restore = %d, want %d", value, write.BusyTimeoutMS)
	}
	check("failed restore")

	close(stop)
	<-done
}

// TestWALRestartCounterIdentities pins the counter semantics with a stub lane:
// an invocation is a check that decided to try, an attempt is a lane that
// actually ran the checkpoint pragmas, and an error is an invocation that
// returned an error or never attempted. A lane that refuses to run (attempted
// false) must not be counted as an attempt or as a restart.
func TestWALRestartCounterIdentities(t *testing.T) {
	t.Run("error before execution", func(t *testing.T) {
		w := &messageWriter{conn: stubLane{frames: 100, outcome: checkpointOutcome{attempted: false}, err: errors.New("lane unavailable")}, limit: 10}
		w.walLimit()
		if got := w.restartInvocations.Load(); got != 1 {
			t.Fatalf("invocations = %d, want 1", got)
		}
		if got := w.restartAttempts.Load(); got != 0 {
			t.Fatalf("attempts = %d, want 0 for a lane that never ran", got)
		}
		if got := w.restartErrors.Load(); got != 1 {
			t.Fatalf("errors = %d, want 1", got)
		}
		if got := w.restarts.Load() + w.restartIncomplete.Load(); got != 0 {
			t.Fatalf("restarts+incomplete = %d, want 0", got)
		}
	})
	t.Run("attempted but refused", func(t *testing.T) {
		w := &messageWriter{conn: stubLane{frames: 100, outcome: checkpointOutcome{attempted: true, completed: false}}, limit: 10}
		w.walLimit()
		if got := w.restartInvocations.Load(); got != 1 {
			t.Fatalf("invocations = %d, want 1", got)
		}
		if got := w.restartAttempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
		if got := w.restartIncomplete.Load(); got != 1 {
			t.Fatalf("incomplete = %d, want 1", got)
		}
		if got := w.restarts.Load(); got != 0 {
			t.Fatalf("completed restarts = %d, want 0", got)
		}
		if got := w.restartErrors.Load(); got != 0 {
			t.Fatalf("errors = %d, want 0", got)
		}
	})
	t.Run("attempted and completed", func(t *testing.T) {
		w := &messageWriter{conn: stubLane{frames: 100, outcome: checkpointOutcome{attempted: true, completed: true, frames: 10, backfilled: 10, passiveNanos: 1000, restartNanos: 2000}}, limit: 10}
		w.walLimit()
		if got := w.restarts.Load(); got != 1 {
			t.Fatalf("completed restarts = %d, want 1", got)
		}
		if got := w.restartAttempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
		if got := w.restartErrors.Load() + w.restartIncomplete.Load(); got != 0 {
			t.Fatalf("errors+incomplete = %d, want 0", got)
		}
		if got := w.restartPassiveNanos.Load(); got != 1000 {
			t.Fatalf("passive nanos = %d, want 1000", got)
		}
		if got := w.restartRestartNanos.Load(); got != 2000 {
			t.Fatalf("restart nanos = %d, want 2000", got)
		}
		if got := w.restartStallNanos.Load(); got <= 0 {
			t.Fatalf("stall nanos = %d, want > 0", got)
		}
	})
	t.Run("below the threshold", func(t *testing.T) {
		w := &messageWriter{conn: stubLane{outcome: checkpointOutcome{attempted: true, completed: true}, frames: 5}, limit: 10}
		w.walLimit()
		if got := w.restartInvocations.Load(); got != 0 {
			t.Fatalf("invocations = %d, want 0 below the threshold", got)
		}
	})
}

// stubLane is a laneConn for the counter tests: it reports a fixed frame count
// and returns a fixed checkpoint outcome.
type stubLane struct {
	frames  int64
	outcome checkpointOutcome
	err     error
}

func (l stubLane) begin(context.Context) (laneTx, error) { return nil, errors.New("stub") }
func (l stubLane) close() error                          { return nil }
func (l stubLane) walFrames() int64                      { return l.frames }
func (l stubLane) backfillRestart() (checkpointOutcome, error) {
	return l.outcome, l.err
}

// TestCheckpointPolicyConfigSemantics pins the knobs' shapes: 0 disables the
// timed fallback (the growth ceiling schedules), 0 on both restores the 1s
// fallback so a run still checkpoints, and invalid values keep the defaults.
func TestCheckpointPolicyConfigSemantics(t *testing.T) {
	t.Run("interval zero leaves the growth ceiling in charge", func(t *testing.T) {
		t.Setenv("CAMPFIRE_CHECKPOINT_MS", "0")
		d := testDB(t)
		if d.checkpoints.interval != 0 {
			t.Fatalf("interval = %v, want 0 (timed fallback off)", d.checkpoints.interval)
		}
		if d.writer.limit <= 0 {
			t.Fatalf("growth ceiling = %d, want the default on", d.writer.limit)
		}
	})
	t.Run("both zero restores the fallback", func(t *testing.T) {
		t.Setenv("CAMPFIRE_CHECKPOINT_MS", "0")
		t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "0")
		d := testDB(t)
		if d.checkpoints.interval != time.Second {
			t.Fatalf("interval = %v, want 1s when nothing else schedules", d.checkpoints.interval)
		}
	})
	t.Run("invalid values keep the defaults", func(t *testing.T) {
		t.Setenv("CAMPFIRE_CHECKPOINT_MS", "-5")
		t.Setenv("CAMPFIRE_CHECKPOINT_PAGES", "not-a-number")
		d := testDB(t)
		if d.checkpoints.interval != time.Second {
			t.Fatalf("interval = %v, want the 1s default", d.checkpoints.interval)
		}
		if d.writer.limit != 10000 {
			t.Fatalf("growth ceiling = %d, want the 10000-frame default", d.writer.limit)
		}
	})
}
