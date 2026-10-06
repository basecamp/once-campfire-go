# Apples to apples: Go doing the Rust reference's work (2026-10-05)

The published Go port was 1.5–2.6× slower than the Rust reference, and doing different work:
other SQL, other caches, `html/template` pages with other bytes, and a different WebSocket
fan-out. Steps 1–4 make Go do the same work as Rust; steps 5–9, guided by profiles of both
applications, take out what the Go port did on top of that work. What remains between them is the
languages, runtimes and libraries rather than the designs or the port.

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
4. **Build and runtime** (8c953b5): a profile-guided build (`cmd/campfire/default.pgo`, from
   the benchmark workloads) and `GOGC=200` by default — Go's counterparts of the reference's fat
   LTO and jemalloc.
5. **Page buffers** (`gc-fixes-20261005`): a rendered page's text buffer goes back to a pool once
   its response is written, and the stylesheets' `Link` header is built once instead of by string
   concatenation on every request. Room pages allocated 116 → 63 KiB per request.
6. **Rows in one cgo call** (`cgo-rows-20261005`): the vendored binding steps and reads the whole
   row in one C call (`third_party/sqlite/row.c`), and the column accessors answer from it unless
   SQLite would convert the value. C calls per request (counted with uprobes): room 875 → 86,
   messages 882 → 90, sidebar 677 → 80, search 389 → 63.
7. **Blocks in place** (`sidebar-20261005`): filter blocks are written into the page where they
   go, and the sidebar's block helpers wrap them there instead of copying them out and back; link
   attributes are built in one hash; signed ids skip the JSON round trip and reuse their HMACs.
   Sidebar pages allocated 100 → 61 KiB per request.
8. **Rows, ETags and cookies** (`cleanup-20261005`): message and membership rows are scanned in
   place into slices sized for the page; the ETag's records are hashed from one reused buffer; the
   signed cookie's HMAC and Base64 alphabet replacer are reused.
9. **Tags in place** (`helpers-20261005`): `ImageTag`, `BuilderTag` and `TurboStreamFrom` build their
   tags in the page (`{%= ImageTag(...) %}`), and the block helpers their opening tags; message
   fragments are looked up by a key built on the stack.

Every step keeps all 221 parity URLs byte-identical to Rust's responses.

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

Steps 5–9 were measured the same way, HTTP only (they don't touch Cable), each step's Go against
the step before it and Rust in one run. Each column is that step's Go; Step 4 is the step 5 run's
baseline (runs on different days differ by a few percent for both applications), and Rust is from
the step 9 run.

| Workload | Step 4 | Step 5 | Step 6 | Step 7 | Step 8 | Step 9 | Rust | Rust / Go |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Room page | 15,046 | 16,233 | 18,254 | 16,475* | 19,293 | 20,177 | 21,777 | 1.08× |
| Messages page | 17,796 | 17,681 | 20,155 | 19,619* | 20,407 | 21,521 | 23,153 | 1.08× |
| Sidebar | 15,238 | 17,840 | 19,577 | 21,156 | 21,967 | 22,508 | 27,371 | 1.22× |
| Search | 20,738 | 22,866 | 24,270 | 22,561* | 24,025 | 24,742 | 27,012 | 1.09× |
| Avatar | 33,549 | 34,015 | 34,969 | 33,300* | 35,215 | 36,106 | 36,919 | 1.02× |
| Static CSS | 208,518 | 204,066 | 205,620 | 141,297* | 199,689 | 206,872 | 256,552 | 1.24× |
| Post message | 7,420 | 7,379 | 7,574 | 7,237 | 7,528 | 7,686 | 7,426 | 0.97× |

\* The step 7 run had host interference in some repetitions (Rust's own static CSS dipped to
119,815); its sidebar row was consistent across two runs. An interleaved A/B of server CPU per
request (four alternating runs of each build) put step 7 at −10% on the sidebar, −1.7% on the room
page and +1.1% on search, within noise. Steps 8 and 9 were checked the same way.

Memory after the HTTP phase is unchanged by steps 5–9: Go 180 MiB, Rust 129 MiB.

The final runs (`final-http-20261006`, `final-20261006`) put the step 4 build (8c953b5) and the
final one against Rust in the same runs:

| Workload | Step 4 | Final | Rust | Rust / Go |
|---|---:|---:|---:|---:|
| Room page | 14,014 | 19,370 | 20,927 | 1.08× |
| Messages page | 17,066 | 20,780 | 22,494 | 1.08× |
| Sidebar | 14,106 | 22,268 | 27,055 | 1.21× |
| Search | 19,925 | 24,522 | 26,047 | 1.06× |
| Avatar | 32,680 | 35,063 | 35,459 | 1.01× |
| Static CSS | 198,636 | 198,673 | 247,738 | 1.25× |
| Post message | 7,342 | 7,421 | 7,315 | 0.99× |
| Cable, 100 clients | 3,999 | 3,965 | 3,771 | 0.95× |
| Cable, 1,000 clients | 496 | 509 | 536 | 1.05× |
| Cable, 1,000, deflate | 208 | 209 | 238 | 1.14× |

HTTP rows are medians of 5 rotating repetitions of an HTTP-only run; Cable rows are from a full run
of 3. In the full run, Go's HTTP repetitions after its first were 5–10% slower than the first (the
step 4 build's too, Rust's not), which put its messages page at 1.18×; the HTTP-only run, whose Go
repetitions agree within 5%, doesn't show that. The application logs are gzipped.

## What the remaining gap is

Both binaries, as benchmarked and under the same harness, were sampled with Linux `perf`
(`cpu-clock` at 1,999 Hz with frame-pointer call stacks, which both arm64 builds keep), and server
CPU per request was read from every thread's `schedstat` in an unprofiled window. Each sample is
charged to GC, the allocator, the kernel, SQLite's C code or cgo when any of its frames is theirs,
and otherwise to the innermost frame's owner (database layer, hashing, templates, HTTP stack, app).
Uprobes on `sqlite3_step` and the column accessors counted the same statements per request in
both applications, and the same column reads on every page but search (where Rust reads a few more). Go minus Rust, µs of server CPU per request, at step 9:

| | Room | Messages | Sidebar | Search |
|---|---:|---:|---:|---:|
| CPU per request, Go / Rust | 142 / 131 | 129 / 121 | 125 / 100 | 109 / 94 |
| Garbage collection + allocator | +8.0 | +7.0 | +13.7 | +4.9 |
| cgo transitions | +3.4 | +3.5 | +3.5 | +2.7 |
| SQLite's C code (same build, same calls) | +1.5 | +1.1 | +2.2 | +4.7 |
| Database layer + hashing | +1.5 | +1.3 | +1.7 | +1.7 |
| Templates and helpers | −0.7 | −4.2 | +1.2 | +0.8 |
| HTTP stack + socket writes | +0.3 | +1.7 | +4.8 | +3.0 |
| Kernel (other) + scheduler | −0.1 | −0.9 | +1.8 | +1.0 |
| App code | −2.9 | −0.9 | −3.7 | −3.5 |
| **Total** | **+11.0** | **+8.6** | **+25.1** | **+15.4** |

- **Garbage collection.** Go collects its 4–5 MB live heap 75–175 times a second at `GOGC=200`
  (measured with `GODEBUG=gctrace=1` after step 7); Rust frees as it goes and reuses memory while
  it is still in cache. What Go still allocates is
  mostly needed (rows' text columns, fragment keys, net/http's headers) and spread over sites of
  1–4 KiB each. A larger heap would buy CPU with memory Go already uses more of.
- **cgo.** About 80 C calls per page at roughly 26 ns each (step 5's profile: 23 µs for 875 calls
  on the room page); rusqlite's are ordinary calls.
- **net/http.** Its user-space work (request parsing, header canonicalization and sanitizing, and
  on the large pages copying the recorded page into one buffer, as it has no vectored writes) costs
  3–7 µs more than hyper's. On the room and messages pages the kernel gives most of it back: one
  write of the assembled page costs it 5–6 µs less than Rust's vectored write of the page's parts.
  A static file is 1.24×.
- **SQLite's own code** runs 1–5 µs slower under Go with identical calls and settings, most on
  search. The profiles show more time in glibc's `malloc` slow path and in locking inside SQLite
  under Go; beyond that it is unexplained.
- Go's app code (routing, controllers, presenters) and the messages page's templates cost less than
  Rust's.

The sidebar's larger gap is the same items over more, smaller pieces of work: a link or button per
room and person, each with its own attribute hash, and a signed avatar id per person.

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
