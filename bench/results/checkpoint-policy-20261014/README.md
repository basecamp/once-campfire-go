# WAL checkpoint policy measurement (2026-10-14, Lever 4)

Development measurement on branch `opt-p3-checkpoint` (worktree
`once-campfire-go-opt9`, from merged train `36bee91`). Native binary, a copy of
the harness seed (`once-campfire-rust/parity/.seed/default`), the harness's own
load generator (`bench/loadgen`) driving `post_message` against the native
server on CPUs 8–11 with the load generator on 12–15. Storage on btrfs (the
harness's own filesystem); the host was busy throughout (1-minute load average
12–20 on 32 CPUs), so runs are reported as repeated interleaved pairs rather
than a single number.

`CAMPFIRE_CHECKPOINT_TELEMETRY=1` was set for both arms; the counters it logs
are always kept (one store per checkpoint) and cost nothing on the request
path.

## Telemetry design

Per PASSIVE checkpoint the checkpointer records, in atomics (no lock on either
side, no per-request work):

- the WAL frame count at the checkpoint (`PRAGMA wal_checkpoint(PASSIVE)`'s
  second column) and its maximum;
- the frames this checkpoint moved (the change in the third column, the
  backfill count — a WAL restart resets it) and the total moved;
- the backlog a reader is still pinning (`frames - backfilled`) and its
  maximum;
- the checkpoint duration, total and maximum;
- the time since the previous checkpoint;
- the `-wal` file size (the WAL's on-disk high-water) and its maximum;
- the busy count (a checkpoint SQLite could not finish).

The write lane separately counts WAL restarts. `CAMPFIRE_CHECKPOINT_TELEMETRY`
prints one line per checkpoint, one per restart and a summary at shutdown; an
in-process snapshot is asserted by the tests.

## What the numbers say

**The fixed one-second interval is not the cost.** Across every run the
checkpointer kept the database backfilled: backlog was 0–15 frames (of ~0.8–1.3
million in the WAL), and the checkpointer's total work over a ten-second window
was 67–451 ms of wall time (a few percent of one core, on the checkpointer's own
goroutine). So changing *when* the interval checkpoints does not buy anything on
btrfs.

**The WAL file is unbounded, and that is the real finding.** With only the
timed PASSIVE checkpoints the `-wal` file grows monotonically for the whole run:
**4.5–5.2 GB in ten seconds** at 8.4 K posts/s (2.8–3.2 GB at 3.2 K posts/s with
one poster). PASSIVE backfills every frame but never resets the WAL, and
SQLite's own automatic restart (`walRestartLog`) needs a window with no readers
in the WAL read slots — with the read pool active on every request, that window
never comes. Over a long-running server this grows without bound (hundreds of
MB/s) until the disk fills.

## A/B: timed PASSIVE only (`CAMPFIRE_CHECKPOINT_PAGES=0`) vs growth ceiling (default 10000 frames)

Interleaved pairs, ten seconds each, c=16, medians of three reps. Raw lines:
Local-only: `conc16.txt`, `conc1.txt`.

| Arm | req/s | p50 | p99 | WAL max | writer restarts / 10 s | checkpointer total |
|---|---:|---:|---:|---:|---:|---:|
| timed PASSIVE only | 8426 | 1.92 ms | 4.94 ms | **4629 MB** | 0 | 298 ms |
| growth ceiling (10000) | **8711** | **1.71 ms** | 9.76 ms | **39.9 MB** | 143 | 123 ms |

Single poster (c=1, two reps): 3168–3232 → 3760–3796 req/s, p50 0.27 → 0.23 ms,
p99 0.67 → 0.48 ms, WAL 2.8–2.9 GB → 39.4 MB.

So the growth ceiling bounds the WAL by ~115× at no measured throughput cost —
the c=16 and c=1 medians both move slightly *in its favour*, within this host's
noise — and it pays about 5 ms of p99 at c=16, where the writer's restart
(PASSIVE then RESTART, waiting for readers) briefly holds the lane. The
checkpointer's own work also drops, because it backfills the same frames in
smaller batches.

## Decision

Change the policy. The timed interval stays as the fallback (the task's "no win
by delaying writeback beyond the timed interval": the ceiling only fires
*earlier*), and the growth ceiling is the new primary schedule. The rollback
switch is `CAMPFIRE_CHECKPOINT_PAGES=0`, which restores the pre-change
behaviour exactly (no restarts, unbounded WAL). The ceiling value follows the
Rust reference's `WAL_LIMIT_PAGES` (10000); 40000 frames (165 MB) was also
measured and showed no advantage beyond a larger bound, so the tighter
reference value is kept.

## Limitations

- One host, one seed, one workload shape (posts). Read-only workloads never
  grow the WAL, so the ceiling never fires there.
- The host was busy; req/s varies by ±5% between adjacent runs, so the
  throughput comparison is directional. The WAL bound is not: it is structural
  (the file stops growing at the ceiling) and reproducible.
- The in-process test asserts the bound with the same mechanism
  (`TestWALStaysBoundedUnderSustainedWrites`); the native runs confirm it on
  btrfs with the real server.

## Review fix (2026-10-14): refused restarts and the pinned-reader stall

The first version read only whether the checkpoint pragmas *executed*, not the
`(busy, log, checkpointed)` row they return. SQLite reports a checkpoint it
refused in that row, so a reader pinning a WAL read slot produced:

- `CheckpointRestart` returned `nil` after **5.004 s** with the reader still
  pinned (the connection's full `busy_timeout`), and
- the writer counted it as a completed restart and retried on the next drain,
  so the stall repeated: 60 posts took **2 m 50 s**, worst post **5.013 s**,
  with **35 "successful" restarts** that never reset the WAL (frames reached
  640, WAL 2.6 MB).

Reproduced on `ab25ea8` with a read transaction held open across sustained
posts (`worst post with a pinned reader took 5.004435181s, want under 1s`).
After the fix, the same scenario: 60 posts in **253 ms**, worst post **251 ms**,
the refused attempt counted as incomplete, and the writer backing off instead of
retrying immediately.

The fix, in three parts:

1. **Read the result row.** `CheckpointRestart` (and the database/sql lane's
   equivalent) reads `(busy, log, checkpointed)` and reports
   `Completed = busy == 0`, so a refused checkpoint is an incomplete attempt,
   not a restart. Counters: `attempts`, `completed`, `incomplete`, `deferred`,
   and the worst stall, in the telemetry summary.
2. **Bound the wait.** The attempt runs with a 250 ms busy timeout and restores
   the connection's 5000 ms, so a pinned reader cannot stall the writer for
   seconds per attempt.
3. **Back off and coordinate.** Refused attempts are spaced by a backoff (25 ms
   doubling to 2 s), and a checkpoint gate shared with the background
   checkpointer keeps a scheduled PASSIVE and a writer RESTART from refusing
   each other (the writer TryLocks and defers, counting the deferral).

Under the normal workload (no pinned readers) the bounded timeout costs
nothing: the same c=16 runs show **attempts 95/96, completed 95/96,
incomplete 0**, worst single attempt 18–30 ms, total writer stall ~1 s per 10 s,
WAL max ~40 MB, and `deferred` 6–16 checks where the checkpointer held the gate.
Local-only raw output: `fix.txt`.

## Second review fix (2026-10-14): pool borrowing, wording, counters

**Pool borrowing.** The database/sql lane's checkpoint set its temporary
250 ms busy timeout on the *pool*, not on a reserved connection, and
`SetMaxOpenConns(1)` serializes individual operations without reserving the
connection across them. A borrower queued on the pool (a session or account
write, a queued after-commit statement) could therefore run between the steps
and inherit the checkpoint's timeout. Reproduced on the pre-fix tree with a
borrower loop reading `PRAGMA busy_timeout`: `checkpoint took 251ms; borrower
read the timeout 3 times` / `borrower observed busy_timeout [250 250] (want
only 5000) over 3 reads`. The lane now reserves one `*sql.Conn` for the whole
sequence and restores the timeout on that connection before returning it; a
failed restore discards the connection (`Raw` with `driver.ErrBadConn`) rather
than returning it misconfigured. After the fix the same probe reads the
configured value every time (the borrower blocks while the connection is
reserved). The fastdb lane already held its connection mutex across the
sequence and is unchanged.

**Wording.** The 250 ms value is a busy-handler setting, not a wall-clock bound
on an attempt, and growth is not bounded while a reader stays pinned. Both are
now stated in the code, the README and here, and the ~40 MB result is qualified
as belonging to this workload and filesystem.

**Split measurement.** The attempt's wall time is now reported split into the
backfill step (copy + sync) and the RESTART step (which includes the busy
wait). At c=16 with the threshold on:

```
invocations=98 attempts=98 completed=98 incomplete=0 errors=0 deferred=6
stall_ms=989 stall_max_ms=27 passive_ms=935 restart_ms=48
per attempt: stall_us=6228..17147  passive_us=6183..17086  restart_us=17..31
```

So ~95% of the writer's time inside an invocation is the PASSIVE backfill
(page copying and its filesystem sync, which the busy-wait setting does not
bound) and ~5% is the RESTART step — whose busy wait was ~20–30 µs, three
orders of magnitude below the 250 ms setting. The setting matters only when a
reader actually pins a slot, which is the case the pinned-reader test covers.
Local-only raw output: `review2.txt`.
