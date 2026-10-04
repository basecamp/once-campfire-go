# Concurrency model and per-request CPU — 2026-10-04

This pass compares the published Go port (`504428a`), the Go port with the changes in
[CHANGES.md](CHANGES.md), and the unchanged Rust reference (`64f8635`). CHANGES.md lists every
change with its measured effect and tests.

## Results

Median requests/sec at 16 HTTP clients over three rotating runs (each application first,
second and third once), two application CPUs. Full report: [application/report.md](application/report.md).

| Workload | Published Go | New Go | Rust | New Go / Rust |
|---|---:|---:|---:|---:|
| Room page | 9,456 | 16,924 | 17,324 | 98% |
| Message history | 13,430 | 19,335 | 18,940 | 102% |
| Sidebar (full page, fair) | 11,315† | 16,034† | 20,436† | 78% |
| Search | 9,073 | 18,800 | 22,942 | 82% |
| Post message | 4,540 | 7,086 | 6,739 | 105% |

† From the separate three-run [sidebar comparison](sidebar-fair/report.md), where all three
applications render the same page. The main run's sidebar rows are 11,934 / 16,446 / 21,002,
but its published-Go sidebar is not comparable: it returned only the 9.5 KB sidebar frame
where Rust returns a 30.8 KB page (see "What was wrong with the sidebar comparison").

p99 latency fell for every route (room 5.29 → 3.41 ms, posting 12.97 → 7.43 ms), but is still
about twice Rust's on reads (Rust: 1.2–1.6 ms). All 9 runs had zero HTTP errors; 377,158
acknowledged writes were verified in both messages and the FTS index; the 9 thumbnails were
byte-identical.

### Where the time went

On reads, both applications kept both CPUs busy (~185–190%), so throughput was set by CPU per
request, not by waiting; Go used 1.4–2.5× Rust's CPU per request. Writes were limited by
Go's concurrency around its single SQLite writer: SQLite's commit-time checkpoints, the
writer connection being handed between request goroutines, and a goroutine per statement for
cancellable contexts. Those now follow the reference's design: a checkpointer beside the
writer, one writer goroutine taking queued transactions, and SQLite work that is never
cancelled. The rest came from cached page shells, fewer allocations and conversions, SQLite
temp B-trees in memory, a cheaper message template and PGO.

### Uncached rendering

Both applications cache rendered fragments (default 32 MB, `CAMPFIRE_FRAGMENT_CACHE_MB`), and
the benchmark's repeated requests keep them warm. With the cache disabled in both
(two interleaved rounds):

| Workload | Published Go | New Go | Rust |
|---|---:|---:|---:|
| Room page | 240–255 | 338–362 | 1,565–1,594 |
| Message history | 254–268 | 357–392 | 1,577–1,603 |
| Sidebar | 5,729–6,149‡ | 5,571–6,034 | 17,591–17,695 |
| Search | 662–739 | 946–998 | 4,204–4,233 |
| Post message | 3,712–3,725 | 5,586–5,741 | 6,539–6,576 |

‡ Bare frame, not comparable. Rendering messages from scratch (rich text and html/template)
is still about 4× Rust's cost. This is the largest remaining gap; it affects first views,
edits and caches smaller than the working set.

### Action Cable

At 1,000 clients without compression, complete deliveries per second were 228 (published Go),
233 (new Go) and 409 (Rust). This pass did not change WebSocket fan-out; it remains open.

### Memory

Pss after the HTTP workloads: 133 MB (published Go), 137 MB (new Go), 102 MB (Rust). A higher
`GOGC` closes much of the read gap but uses about twice the memory, so it was not adopted
(see CHANGES.md).

## What was wrong with the sidebar comparison

The harness checks the sidebar by its room IDs, not by the page it returns. For
`GET /users/me/sidebar` without a `Turbo-Frame` header, Rails and Rust render the full page
layout around the sidebar frame (30.8 KB on the seed), and for frame requests a minimal frame
layout (10.4 KB). The Go port returned only the bare `<turbo-frame>` (9.5 KB) in both cases,
so it rendered about a third of what Rust did. Every earlier Go sidebar figure, including the
published 24,658 vs 38,303 req/s, therefore overstated Go. The Go sidebar now renders the
same layouts; its tag structure matches Rust's and only whitespace differs.

The other measured pages contain the same tags and attributes in both applications; Rust's
responses are about 12% larger because of indentation (room page 416 vs 374 KB), which costs
Rust slightly more to send.

## Fairness and method

- Both applications are native release builds from the pinned toolchains (Go 1.27.1 with
  `cmd/campfire/default.pgo`; Rust 1.98.1) on Debian trixie with the same libvips, run on
  fresh copies of the same parity seed.
- Application on CPUs 0–1 with `GOMAXPROCS=2`, `TOKIO_WORKER_THREADS=2` and
  `RAILS_MAX_THREADS=2` (two SQLite readers each); the reference load generator on CPUs 2–3.
  Identity encoding, 2-second warmups, 5-second samples, three rotating repetitions.
  `bench/application` gained `--workers` so these counts follow the CPU set.
- Response contracts (message/room IDs), write persistence and FTS indexing, complete Cable
  delivery and thumbnail bytes are checked on every run.
- After every change, the room, history, sidebar and search responses of the new Go binary
  were compared byte for byte with the published binary's (normalizing the listener port and
  load timestamp): identical, except the intentional sidebar layout change.
- The per-change measurements and the uncached table come from a small script (not committed)
  that runs the same load generator and reports the server's CPU per request from `/proc`;
  its runs are interleaved but always in the same order.

## Limitations

- Docker Desktop's Linux VM (linuxkit 7.0, 4 vCPUs) on an Apple M4 laptop, not the earlier
  16-CPU Linux workstation, so absolute numbers are not comparable with earlier reports. The
  host's Xcode license was not accepted, which blocked native macOS builds.
- About 25 unrelated containers (Postgres, Supabase services) ran in the same VM at under
  20% of one CPU in total; interleaving spreads this across applications.
- Two application CPUs only; 1-, 64-client and 10,000-client Cable workloads, TLS and public
  compression were not measured.
- Browser workflows, screen comparison and the Go/Rust upgrade check were not rerun (they need
  Playwright and a separate Rust checkout). Package tests, vet and race tests pass.

## Reproduce

```sh
bench/application --out bench/results/my-run --apps go-before go rust \
  --baseline-go /path/to/published-go-binary \
  --routes room_show messages_page sidebar search post_message --concurrency 16 \
  --cable-clients 1000 --deflate 0 --upload-reps 1 --reps 3 --seconds 5 \
  --server-cpus 0-1 --loadgen-cpus 2-3 --workers 2
```

Only the harness reports are committed; per-run samples, metadata and logs were left out of
the repository. The same comparison built without PGO measured room 16,540, history 19,116,
search 18,662 and posting 6,770 req/s.
