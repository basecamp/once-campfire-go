# Apples to apples: Go doing the Rust reference's work (2026-10-05)

The published Go port was 1.5–2.6× slower than the Rust reference, and doing different work:
other SQL, other caches, `html/template` pages with other bytes, and a different WebSocket
fan-out. These four steps make Go do the same work as Rust, so that what remains between them is
the languages, runtimes and libraries rather than the designs.

1. **Database** (e4c6145): SQLite's C API through a vendored `crawshaw.io/sqlite` with the
   reference's SQLite 3.53.2 and build options, instead of `database/sql` and mattn; the
   reference's single writer goroutine and off-writer checkpointer; message writes with the
   reference's statements and after-commit order.
2. **Pages** (merge e40f764, cleanup cd6e671, fix 67ab899): every template converted from the
   reference's askama views to quicktemplate with its bytes intact, the reference's view models,
   helpers, fragment cache, recorded pages and part-based ETags, and the reference's SQL for the
   benchmarked routes. All 221 URLs in `bench/parity-urls/` are byte-identical to Rust's
   responses; response sizes in the benchmark equal Rust's.
3. **Action Cable** (6b7c9f5): the reference's fan-out — subscribers indexed by stream,
   authorization at subscribe time with the reference's revocations, one shared heartbeat, and
   up to 64 frames per vectored write.
4. **Build and runtime** (this commit): a profile-guided build (`cmd/campfire/default.pgo`, from
   the benchmark workloads) and `GOGC=200` by default — Go's counterparts of the reference's fat
   LTO and jemalloc.

## Results

Docker on an Apple M4 Pro (6 VM CPUs): server on 3 CPUs with 3 workers, load generator on the other
3, 16 HTTP clients, identity encoding, the same seed (`bench/seed`) for both applications. Each
column is the median of 3 rotating repetitions from that step's report; Rust is from the step 4 run
(it was stable across all runs). Requests/s, or complete broadcasts/s for Cable.

| Workload | Published Go | Step 1 | Step 2 | Step 3 | Step 4 | Rust | Rust / Go |
|---|---:|---:|---:|---:|---:|---:|---:|
| Room page | 11,947 | 13,579 | 12,277 | 12,509 | 14,888 | 21,675 | 1.46× |
| Messages page | 16,411 | 18,625 | 15,655 | 15,083 | 17,885 | 23,290 | 1.30× |
| Sidebar | 14,238 | 21,849 | 11,131 | 10,480 | 14,794 | 27,518 | 1.86× |
| Search | 12,101 | 13,937 | 17,657 | 17,868 | 20,934 | 27,449 | 1.31× |
| Avatar | 27,559 | 31,336 | 30,576 | 29,621 | 34,315 | 35,944 | 1.05× |
| Static CSS | 188,908 | 192,455 | 182,872 | 181,886 | 204,248 | 258,114 | 1.26× |
| Post message | 4,699 | 7,359 | 7,348 | 7,155 | 7,374 | 7,458 | 1.01× |
| Cable, 100 clients | 1,793 | 2,177 | 2,151 | 3,960 | 4,050 | 3,755 | 0.93× |
| Cable, 1,000 clients | 261 | 277 | 260 | 495 | 499 | 532 | 1.07× |
| Cable, 1,000, deflate | 212 | 211 | 205 | 206 | 208 | 240 | 1.15× |

The published Go column ran on the first build of the seed, whose whole-second timestamps carried a
`.000000` fraction; the seed was rebuilt with Rails' format before step 2 (same rows and labels).
Step 2's column is after its signed-id fix (the step 3 report's baseline). Pages got slower in step
2 because Go then started doing Rust's work: the layout is rendered on every request instead of
spliced from a Go-only cached shell, and the sidebar is the full 30,763-byte page Rust sends rather
than a 9,462-byte frame. Only Step 2 onwards compares like with like.

Memory after the HTTP phase (Pss): step 3 Go 142 MiB, step 4 Go 181 MiB (the `GOGC=200` trade),
Rust 128 MiB. Posting and Cable are writer- or client-bound and now match Rust; Go's write p99
(7.1 ms) and Cable p99 at 1,000 clients (38 ms) are lower than Rust's (8.6 ms, 47 ms). Compressed
broadcasts are limited by the load generator inflating every frame on 3 CPUs for both applications.

## What the remaining gap is

Profiles of step 2 and later (room page and sidebar) show where Go spends the time Rust doesn't:

- **Calls into C.** Each SQLite call crosses cgo (tens of nanoseconds each); a room page reads 40
  rows with several column calls per value, and the sidebar more. rusqlite's calls are ordinary
  function calls.
- **Allocation and garbage collection.** 15–20% of page CPU at `GOGC=100`; `GOGC=200` buys most of
  the step 4 gain with memory.
- **net/http.** No vectored writes, so a page's recorded parts are copied into one buffer before the
  write (about 6% of the room page); a trivial static file is 1.26× slower than hyper's.
- **Templates and helpers.** quicktemplate writes through `io.Writer` interfaces and the helpers
  build strings; askama's generated code writes into a `String` with monomorphized helpers.

The micro-tuning left out by design (regular-expression routing, timestamp parsing from borrowed
text, key formatting) accounts for a few percent more. Everything else — queries, caches, bytes,
fan-out — is now the same in both programs.

## Reproduce

```sh
go run ./bench/seed --out ../once-campfire-rust/parity/.seed/default \
  --fixtures ../once-campfire-rust/reference/test/fixtures --env ../once-campfire-rust/parity/.env.reference
bench/parity --rust ../once-campfire-rust/target/release/campfire --urls <(cat bench/parity-urls/*.txt)
bench/application --out bench/results/my-run --apps go rust --reps 3 --seconds 5 --concurrency 16 \
  --cable-clients 100 1000 --deflate 0 1 --upload-reps 3 --server-cpus 0-2 --loadgen-cpus 3-5 --workers 3
```

The 10,000-client Cable test was left out: with 3 load-generator CPUs the client can't read every
frame, so Rust's run failed its complete-delivery check (`apples-step1-20261005-aborted`).
