# opt-p1-write — writer-owned after-commit second phase

Branch `opt-p1-write`, worktree `/home/sebishogun/Work/Development/once-campfire-go-opt7`,
from the merged train `36bee91`. Two changes:

1. **Lever 1** — the queued write path's after-commit work (search-index insert + unread bump) is
   now a writer-owned second phase: after the primary group-commit, the writer runs the batch's
   after work in bounded transactions (`afterBatchLimit = 256` jobs each, one savepoint per job
   when the chunk holds more than one), before delivering results. A batch of B messages costs one
   primary commit plus `ceil(B/256)` after commits instead of one after transaction per caller
   (B+1).
2. **Lever 6** — the create transaction's membership count and creator name are one statement
   (`stmtAuthorizedCreatorName`); the plain in-line statement count drops 7→6 (POST /messages
   12→11 on the database/sql lane, 5 unchanged on the direct lane).

Durability is unchanged (WAL, `synchronous=NORMAL`, `wal_autocheckpoint=0` on the writer, the
off-writer PASSIVE checkpointer, `journal_size_limit` untouched). No new fsync suppression.

## Commits per acknowledged message

Measured in-process from the writer's own batch histogram
(`TestWriteQueueCommitMeasurement`, `CAMPFIRE_MEASURE_COMMITS=1`): one run yields both the old
per-caller shape (primary batches + one after commit per message) and the writer second phase
(primary batches + after chunks), so no cross-binary comparison is needed. Host load 15–19.

| Messages | Primary batch sizes | Commits before (B+1 shape) | Commits after (second phase) |
|---:|---|---:|---:|
| 600 | `[1, 599]` | 602 (1.0033/message) | 6 (0.0100/message) |
| 2000 | `[1, 1999]` | 2002 (1.0010/message) | 11 (0.0055/message) |

The queue-off path is one `BEGIN IMMEDIATE`/`COMMIT` per request (1 commit/message), pinned by
`TestFastWriteStatementStream`'s in-line stream and the POST /messages statement count.

## Queue on/off throughput (same binary)

`/tmp/opencode/measure_queue.py`: the Go binary built from this worktree, run against a fresh copy
of the Rust harness seed (`once-campfire-rust/parity/.seed/default`) per run, `GOMAXPROCS=4` on
CPUs 8–11, reference loadgen on 12–15, `--conc 16`, 5-second samples, 6 alternating repetitions.
`CAMPFIRE_WRITE_QUEUE=on` (new code) vs `off` (per-request transaction), everything else identical
(`CAMPFIRE_FASTDB=on`, `CAMPFIRE_FASTDB_WRITE=on`, checkpoints pinned to 3600000 ms). All responses
200, zero transport errors.

| Mode | rps median (min–max) | p50 ms median (min–max) | p99 ms median (min–max) |
|---|---:|---:|---:|
| queue on | 14441.9 (6873.1–18033.2) | 0.790 (0.714–0.832) | 2.482 (2.375–2.651) |
| queue off | 7469.1 (7000.9–7815.7) | 1.566 (1.489–1.684) | 8.927 (8.767–9.543) |

Noise: host load average 15.0–18.5 throughout. The `off` arm is tight (rps spread ±5%). The `on`
arm has one low outlier (6873 rps, load 18.5) and otherwise 13.3k–18.0k; the median 2x and the p50/p99
gaps are far larger than the spread, so the direction is resolved even though the `on` magnitude is
not. The commit counts above are exact and scheduling-independent, which is the load-bearing result.

## Limitations

- The A/B is one workload (concurrent plain text posts) on one host under sustained background load;
  it is not a capacity claim for either mode.
- The commit identity (batches + after chunks) is exact; the batch-size distribution is
  scheduling-dependent and is recorded per run rather than pinned.
- The 10-second after-phase statement deadline is unchanged from the per-caller shape; a hung begin
  still blocks the lane (the writer now owns that risk) — begin is left on `context.Background()`
  to match the previous shape.
