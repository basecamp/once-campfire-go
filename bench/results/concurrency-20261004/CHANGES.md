# Change log: concurrency model and per-request CPU — 2026-10-04

Each change below lists what it does, why, the Rust behavior it follows, the tests that
cover it, and its measured effect at the time it was made. Measurements are sequential,
interleaved runs on Docker Desktop (linuxkit 7.0, Apple M4 host, 4 VM CPUs): application on
CPUs 0–1 with 2 workers/readers (`GOMAXPROCS=2`, `TOKIO_WORKER_THREADS=2`,
`RAILS_MAX_THREADS=2`), the reference load generator on CPUs 2–3, 16 HTTP clients,
identity encoding, fresh copies of the parity seed, 5-second samples after a 1-second
warmup. "CPU/req" is the server process's user+system CPU divided by completed requests.
Single samples vary by about ±5% (sidebar up to ±12%); the final rotating comparison is in
[README.md](README.md).

## Where the gap was

On every read route both applications kept their two CPUs busy (≈185–190%), so reads were
limited by CPU per request, not by waiting: Go spent 1.4–2.5× Rust's CPU per request.
Writes were the exception: Go used only ≈130% CPU, its single SQLite writer being the
bottleneck. Starting point:

| Route | Go req/s | Go CPU/req | Rust req/s | Rust CPU/req |
|---|---:|---:|---:|---:|
| Room page | 8,803–9,226 | 199 µs | 16,420–17,127 | 111 µs |
| Message history | 12,867–13,205 | 141–145 µs | 18,400–18,684 | 100 µs |
| Sidebar | 11,546–12,883 | 142–152 µs | 20,052–20,760 | 93 µs |
| Search | 8,303–9,077 | 206–221 µs | 22,394–22,795 | 84 µs |
| Post message | 4,029–4,531 (p99 13–14.5 ms) | 291–316 µs | 6,830–7,262 (p99 6.5–7.6 ms) | 208–215 µs |

## 1. WAL checkpoints beside the writer (`internal/database/wal.go`, `wal.c`, `drivers.go`)

SQLite's auto-checkpoint made the commit that crossed 1,000 WAL pages checkpoint and fsync
before returning, while every queued write waited (`Commit` was 33% of the old write
profile). As in `reference/crates/db/src/database.rs`, the writer connection's WAL hook now
only records the WAL size; each 1,000 pages wake a checkpointer goroutine with its own
connection (PASSIVE), and at 10,000 pages the writer restarts the WAL itself (RESTART,
after any running checkpoint). `journal_size_limit` is 64 MiB as in the reference.
go-sqlite3 has no WAL-hook API: an auto-extension registers a `campfire_wal_hook()` SQL
function and the writer driver's connect hook calls it (SQLite installs its default
auto-checkpoint after auto-extensions run).

- Post message: 4,530 → 5,330 req/s (+18%), p99 13.0 → 10.8 ms. Reads unchanged.
- Tests (ports of the reference's): `TestTheCheckpointerCopiesTheWALIntoTheDatabase`,
  `TestTheWALStaysBoundedUnderSustainedWrites`, `TestTheBundledSQLiteHasTheWALResetFix`.

## 2. SQLite work is not cancelled (`internal/database/uncancelled.go`, `read_pool.go`)

With a cancellable request context go-sqlite3 runs every `Exec` on a new goroutine and
registers a context watcher for every query; database/sql adds a watcher goroutine per
transaction and result set. The reference never cancels SQLite work ("once queued, f runs
even if its caller stops waiting"). Reads and the writer driver now pass a context without
cancellation; waiting for the writer stays cancellable.

- Reads: room 199 → 182 µs, messages 141 → 128 µs, sidebar 152 → 130 µs, search
  221 → 189 µs CPU/req (+8% to +17% req/s).
- On its own this cut write throughput (≈4,640 vs ≈5,200 req/s with checkpoints alone):
  cheaper reads meant more runnable goroutines between the writer's hand-offs. Change 3
  removes those hand-offs.
- Test: `TestAReadGivenUpWhileItWaitsStillRuns` (port of
  `a_read_given_up_while_it_waits_still_runs`).

## 3. One writer goroutine (`internal/database/database.go`)

The single writer connection used to be handed by database/sql's pool to each request's
goroutine in turn, so it sat idle until that goroutine was scheduled, and every busy read
lengthened the wait. As with the reference's writer thread, `Transaction` now queues its
function (bounded queue of 256) for one writer goroutine that runs transactions back to
back; a panic rolls back and is raised in the caller.

- Post message: ≈5,080 → ≈6,115 req/s (+20%), p99 11.2 → 8.5 ms; writer CPU use
  rose from ≈155% to ≈170% of the two CPUs.
- Tests: `TestAPanickingWriteRollsBackAndLeavesTheWriterUsable` (ports of
  `a_panicking_write_rolls_back` and `a_panicking_write_leaves_the_writer_usable`),
  `TestAFailingWriteRollsBack`, `TestConcurrentWritesAllCommit`, `TestAWriteAfterCloseFails`.

## 4. Search page shell cache; no unused rooms query (`room_shell.go`, `server.go`)

Search rendered its whole layout template per request while the room page reused a
cached shell. Search now uses the same shell cache. Its handler also loaded every room of
the user for a field no search template reads.

- Search: 191 → 161 µs CPU/req (+17%).
- Bug caught while extending the tests: the search navigation shows the result count, which
  the shell cache would have frozen. The count is now part of the shell key and comes from
  `page.MessageCount`. Test: `TestSearchShellPreservesBytesAndRequestData` (result counts,
  queries, recent searches, user, flash, styles, frame) beside the room shell test.

## 5. Shells split once, with cached digests (`room_shell.go`, `recorded.go`)

Every cached-shell response ran `strings.ReplaceAll` over the shell (≈20 µs), copied it to
`[]byte` twice and SHA-256-hashed it for the ETag. Shells are now split once into static
segments with their digests and slots for the messages and load time; responses assemble
the parts and hash only the per-request ones (the ETag stays a digest of all part
digests, like the reference's `Body::Parts`).

- Room: 10,389 → 12,915 req/s (175 → 138 µs CPU/req, +24%). Search → 15,550 req/s.
- Tests: shell tests check every part's digest and the bytes against the uncached template.

## 6. Fewer allocations on message lists (`server.go`, `fragments.go`, `messages.go`, `models.go`)

- Room, history and search pages pass message rows directly instead of wrapping them in
  view structs that were immediately unwrapped (17% of room-page allocation).
- Message references are allocated at their page size (28% of room-page allocation).
- Message-list cache keys are appended into one buffer.
- `Last-Modified`/`If-Modified-Since` are parsed only when present (empty strings were
  parsed with three layouts on every response).
- Search loads only IDs and timestamps (`SearchReferences`), like the room page and the
  reference; uncached fragments load the rest as before.
- Room 13,774 → 15,503 req/s (133 → 118 µs), history 14,972 → 16,398 (124 → 115 µs),
  search 16,244 → 17,851 (115 → 105 µs).
- Test: `SearchReferences` returns the same messages as `Search` in the lifecycle test.

## 7. SQLite temp B-trees in memory; no per-call connection mutex (`drivers.go`, `database.go`)

Small `DISTINCT`/`ORDER BY` queries spent most of their time creating temporary B-trees in
the file temp store: the sidebar's two placeholder queries took 31.7 µs together, 11.4 µs
with `temp_store=MEMORY` (Go benchmark on the seed). Connections also used SQLite's
serialized mode; as in the reference (`SQLITE_OPEN_NO_MUTEX`) they now don't, since each
connection is used by one goroutine at a time (11.0 µs for the same queries).

- Sidebar: 13,179 → 15,781 req/s (137 → 119 µs CPU/req).
- Batching the sidebar's per-direct-room member queries into one (same order, from the
  same index) was within run-to-run noise. Test: `TestRoomMembersByRoomMatchesEachRoom`.

## 8. Message partial (`quick_boosts.go`, `templates/messages.html`, `rails/verifier.go`)

Rendering one message took 65 µs and 1,045 allocations, mostly html/template reflection:

- The eight quick-boost forms are rendered once from the unchanged `quick-boosts`
  template with marker values and filled in per message, escaped as html/template escapes
  attribute values. Test: `TestQuickBoostsMatchTheTemplate` compares against executing the
  template for 268 client IDs (all bytes, NUL, invalid UTF-8, quotes, markers) × 6 IDs.
- Message fields are read as `.Message.ID` etc.: promoted fields made text/template search
  embedded structs on every access.
- Regular expressions compiled on each signed-ID (avatar) and link check are compiled once.
- `BenchmarkMessageUncached`: 65.3 → 30.0 µs, 1,045 → 424 allocations per message.
- Post message: 6,004 → 6,849 req/s (284 → 245 µs CPU/req).

## 9. Timestamps (`conditional.go`, `database.go`)

History pages built their ETag from per-message `fmt.Sprintf`/`Format`/`ReplaceAll`;
timestamps were parsed by trying layouts and re-formatted for cache keys. The same bytes
are now appended directly, and the stored `YYYY-MM-DD hh:mm:ss.ffffff` form is parsed
without `time.Parse` (other forms still use it).

- History: 16,317 → 19,024 req/s (116 → 99 µs CPU/req); room 15,681 → 16,255; search
  17,654 → 18,547.
- Tests: `TestMessageFreshnessKeyMatchesJoinedCacheKeys` (the previous implementation as
  the reference), `TestStampParsingAndFormattingMatchTime` (against `time.Parse`/`Format`).

## 10. Sidebar renders the reference's page (fixes an unfair comparison)

`GET /users/me/sidebar` without a `Turbo-Frame` header returns, in Rails and Rust, the full
page layout around the sidebar frame (30.8 KB on the seed); with the header, a minimal
`<html><head></head><body>` frame layout (10.4 KB). Go returned only the bare
`<turbo-frame>` (9.5 KB) in both cases. The benchmark's sidebar contract compares room IDs,
not page shape, so **every earlier Go sidebar figure, including the published 24,658 vs
38,303 req/s, measured Go rendering about a third of what Rust rendered.**

The sidebar template now uses the shared layout (frame or full page, as in the reference);
its tag structure matches Rust's in both modes and only whitespace differs (29.7 vs 30.8 KB,
9.5 vs 10.4 KB). The old sidebar cache key covered only sidebar data, which no longer
determines the output (account, flash, platform and frame mode do too), so the sidebar now
uses the page-shell cache keyed by the whole page. Test: `TestSidebarShellTracksRenderedChanges`
(the old cache-key test's eleven changes plus frame mode and flash, checked against the
uncached template).

Fair comparison, three rotating runs where all three applications render the full page
([sidebar-fair/report.md](sidebar-fair/report.md); "published Go" is the published code with
only this template change, built for this measurement):

| | Published Go (full page) | New Go | Rust |
|---|---:|---:|---:|
| Sidebar req/s | 11,315 (10,315–11,590) | 16,034 (15,690–16,092) | 20,436 (20,397–20,870) |
| p99 ms | 4.39 | 3.48 | 1.30 |

New Go is +42% over the fair baseline and at 78% of Rust. In the main comparison the
published binary's sidebar row still returns the bare frame and is marked not comparable.

## 11. Profile-guided optimization (`cmd/campfire/default.pgo`)

`go build` uses `cmd/campfire/default.pgo` automatically (`-pgo=auto`): a merged CPU profile
of 10-second room, history, sidebar, search and message-post workloads from `bench/profile`
on the final code. In two interleaved rounds against the same code built without it:
history 100–101 → 96–97 µs CPU/req (−3.5%), room, sidebar and search about −1%, posting
mixed (−4% and +1%). Regenerate it after large changes:

```sh
for w in room search sidebar write; do bench/profile $w --out prof --seconds 10; done
bench/profile room --path "/rooms/$ROOM/messages?before=$MESSAGE" --out prof-history --seconds 10
go tool pprof -proto prof/*.pprof prof-history/room.pprof > cmd/campfire/default.pgo
```

## Common Go performance advice, checked

Measured profiles and benchmarks drove every change. Preallocation, `strings.Builder`/append
buffers, hoisting work out of loops (regexes, layouts), avoiding reflection and `fmt` on hot
paths, a bounded worker queue (the writer), and PGO are applied above. Response buffers
were already pooled with `sync.Pool`. cgo row reads were already batched by the driver; the
remaining cost was SQLite's own work, addressed in change 7. Lock contention was negligible
in profiles. `GOMAXPROCS` follows the container's CPU limit in Go 1.25+, and the benchmark
sets it explicitly for both applications. Go 1.27.1 is already the newest release. Stripping
symbols (`-s -w`) affects size, not speed; the production image already strips them.

## Not adopted: a larger GOGC

GC was ≈10% of read CPU. With the changes up to 5, `GOGC=200` gave +14% on room pages at
183 MB RSS (vs 142 MB) and `GOGC=400` reached Rust's room throughput at 270 MB (Rust:
106 MB). This trades memory rather than removing work, so the default stays; it remains an
operator setting.

## Response equivalence

After each change the room, history, sidebar and search responses of the new binary
were compared byte for byte with the starting binary on the parity seed (after
normalizing the listener port and the room's load timestamp): identical every time.
