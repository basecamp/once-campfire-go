# Go/Rust HTTP throughput comparison, 2026-10-05

The optimized Go port exceeds Rust's median throughput on each of the five requested
HTTP workloads in the final three-repetition comparison. `bench/target final` passes
all five. This is a measured throughput result, not a latency or memory win.

| Workload | Go req/s | Rust req/s | Go advantage |
|---|---:|---:|---:|
| Room page | 9,570 | 8,533 | +12.1% |
| Message history | 12,028 | 9,214 | +30.5% |
| Sidebar | 16,187 | 12,569 | +28.8% |
| Search | 12,261 | 9,258 | +32.4% |
| Post message | 3,483 | 3,438 | +1.3% |

[Full report](final/report.md), [raw samples](final/raw.json),
[metadata and source hashes](final/metadata.json), [acceptance output](final/target.md),
and [harness hashes/build commands](final/harness.json).

## What changed and why it belongs in the app

- Combine fresh authentication/activity reads, room authorization/invitation reads,
  and message membership/creator reads. Sidebar data now comes from one consistent
  database snapshot rather than repeated queries for individual rooms and users.
- Batch row transport across CGO and retain bounded decoded snapshots, checking
  SQLite `data_version` before every lookup. Dedicated observers match read-pool
  concurrency and compare versions only on the same connection. Every committed
  change, including another process's edits, invalidates the cache. Authentication
  and individual room authorization remain uncached; callers cannot mutate cached
  account settings or returned rows.
- Retain static room/search HTML chunks and hashes, render messages from slots
  compiled from the original template, cache message validators, and allocate views
  only when rendering is required. Standard escaping and URL filtering remain;
  boosts and changed templates fall back to general `html/template` execution.
- Coalesce response parts and send known `Content-Length` for complete buffered
  bodies. Avoid repeated path escaping and regex checks for unrelated route prefixes.
- Move WAL checkpoints off the writer. Poll pending pages without copying them,
  checkpoint after 1,000 pending pages or one second of light traffic, and restart
  at 10,000 pages so sustained writes do not repeatedly checkpoint on commit.
  The writer keeps a 10,000-page backstop and `synchronous=NORMAL`.
- Use one quick-reaction form per message, with each of the eight buttons submitting
  its own value. This preserves every reaction control and Turbo target while
  eliminating repeated forms and hidden inputs. First-sample history HTML fell from
  342,444 bytes in the unchanged Go port to 272,924 bytes. This is an intentional
  markup difference, covered by real browser submissions and live delivery.

There are no special cases for benchmark paths, fixture IDs, user names, query text,
load-generator headers or clients. The normal build uses no PGO profile. The Rust
submodule, schema, dependencies, frontend JavaScript and CSS were not changed.

## Correctness

- `bin/check`: gofmt, asset consistency, vet, all package tests with `-race`, including
  the vendored WebSocket module. [Log](validation/race-vet.log.gz).
- Added tests compare slot-rendered HTML against `html/template` across attachments,
  emoji, boosts and escaping cases; compare cached page bytes and search counts;
  verify grouped sidebar ordering; and exercise external edits, revoked memberships,
  account settings isolation and background checkpoint persistence.
- A live HTTP test verifies full GET bodies and HEAD lengths without chunked framing.
  [Response race tests](validation/response-race.log.gz).
- `golangci-lint run --build-tags sqlite_fts5 --new-from-rev=HEAD`: zero new issues.
  [Log](validation/lint.log.gz).
- `bin/check-upgrade --rust-root reference`: cookies accepted in both directions,
  and Go-written messages/FTS read by Rust. [Log](validation/upgrade.log.gz).
- Chromium smoke test: setup, two-tab messaging, all eight reaction submissions and
  broadcasts, edits/search, account/profile changes, room operations, bot APIs,
  styles, session transfer, invitations and direct pings. [Log](validation/browser.log.gz).
- All six measured application runs completed with zero HTTP errors. All
  136,104 acknowledged writes, including warmup,
  were counted in both messages and FTS. Six real thumbnails had identical bytes.
  Message/room IDs matched across representative responses, and the seed was unchanged.

## Reproduce

Go base commit: `504428addff333549f1fc88b003c7331779a3c2a`.
Rust: `64f86353021145b63849fb1cd93adeb08f3b8dbb`; use the pinned submodule and its parity seed.
Go 1.27.1, Rust 1.98.1, CGO and FTS5 are required, plus the native libraries in README.

```sh
git submodule update --init --recursive
PATH="/home/codex/.local/share/mise/installs/go/1.27.1/bin:$PATH" bin/build
(cd reference && mise exec rust@1.98.1 -- cargo build --release -j 8)
(cd reference && mise exec rust@1.98.1 -- cargo build --release \
  --manifest-path bench/loadgen/Cargo.toml --target-dir target/bench)
# Prepare reference/parity/.seed/default using its documented Rails seed builder.
# This run used the pinned Rails source with the matching Campfire Docker image.
sudo renice -n -10 -p $$
bench/application --rust-root reference --out bench/results/my-comparison \
  --apps go rust --routes room_show messages_page sidebar search post_message \
  --concurrency 16 --cable-clients --upload-reps 1 --reps 3 --seconds 5
bench/target bench/results/my-comparison
```

Four application workers and CPUs 8–11; load generator on CPUs 12–15. Each sample
uses five seconds after a two-second warmup, identity encoding, the direct HTTP/1.1
listener, and a new copy of the same seed. Go/Rust order alternates. Reproduction
on another machine should choose disjoint CPU sets suitable for that machine.

## Limits and exploratory measurements

The write advantage is only 1.3%; ranges overlap (Go 3,344–3,504/sec, Rust
2,904–3,581/sec). Treat writes as near parity pending longer measurements on a
quiet host. Read advantages are 12–32% in this run. These numbers compare these
implementations on this VM, not the languages generally or previous machines.

Go still has higher p99 latency on all five workloads and higher HTTP Pss
(123.9 versus 101.1 MiB). Write p99 is 18.9 versus 13.1 ms; room p99 is 7.6
versus 3.6 ms. Raising throughput did not resolve tail latency or prove memory
superiority. Native media output uses libvips 8.15.1, whereas container goldens
pin 8.16.1; those container goldens were not rerun in this pass.

Cable throughput, other HTTP concurrency levels, public TLS/compression throughput,
and full HTML/network parity are outside the five-workload target. Grouped reaction
forms deliberately change markup while preserving behavior. No response was made
incomplete for timing. [Host activity record](final/CONTENTION.md).

`baseline` retains the initial three-repetition comparison. `step*` directories retain
intermediate results, including failed targets; `final-contended` is an interrupted
attempt. `pgo-trial` was exploratory, did not give useful gains, and its initial room
sample overlapped a diagnostic profile; it is excluded from acceptance. No PGO was
retained. Final acceptance uses only `final`, the sole complete comparison of the
final application binaries. Repetitive server logs are gzip-compressed without
editing; raw JSON and metadata remain uncompressed.
