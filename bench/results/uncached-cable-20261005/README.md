# Uncached rendering, Cable fan-out, memory and latency — 2026-10-05

This pass follows [#3](https://github.com/basecamp/once-campfire-go/pull/3)
([report](../concurrency-20261004/README.md)) and compares its code ("before"), this code
and the unchanged Rust reference in the same environment: two application CPUs with two
workers and two SQLite readers each, the reference load generator on two other CPUs, fresh
copies of the parity seed. [CHANGES.md](CHANGES.md) lists each change with its measured
effect and tests.

## Uncached rendering

Both applications with `CAMPFIRE_FRAGMENT_CACHE_MB=0`, requests/sec at 16 clients,
interleaved:

| Workload | Before | After | Rust | After / Rust |
|---|---:|---:|---:|---:|
| Room page | 346 | 984 | 1,584 | 62% |
| Message history | 364 | 1,074 | 1,580 | 68% |
| Search | 935 | 2,282 | 4,171 | 55% |
| Sidebar | 5,415 | 6,123 | 17,822 | 34% |
| Post message | 5,350 | 7,254 | 6,680 | 109% |

Rendering a message without the cache now costs about a third of what it did. Messages
render through a compiled renderer instead of html/template, autolinking skips text
without URLs, uncached messages share batched queries, and rich-text processing allocates
less. The uncached sidebar still renders the page layout through html/template on every
request. That layout is normally served from the page cache.

## Cached workloads and Cable

Three rotating runs ([report](application/report.md)):

| Workload | Before | After | Rust |
|---|---:|---:|---:|
| Room page | 16,707 | 16,583 | 17,040 |
| Message history | 19,494 | 19,281 | 18,545 |
| Sidebar (full page) | 16,335 | 16,599 | 20,713 |
| Search | 18,071 | 17,958 | 22,106 |
| Post message | 6,740 | 8,301 | 6,700 |
| Cable, 1,000 clients (messages/s) | 226 | 389 | 403 |
| Cable, 1,000 clients, compressed | 151 | 150 | 168 |

- **Cached reads** are unchanged within run-to-run noise. CPU per request matches #3 in
  interleaved runs. The sidebar includes Go's in-memory SQLite temp store, which the reference
  doesn't use. With Rust's file temp store, Go's sidebar measured 13,671 req/s (66% of Rust's
  20,643; [details](../concurrency-20261004/temp-store/report.md)).
- **Posting** is 23% faster than #3 and 24% faster than Rust, because a new message is no
  longer rendered through html/template.
- **Cable fan-out** without compression went from 56% to 96% of Rust's rate, and per-client
  p99 delivery latency fell from 149 to 40 ms (Rust: 53 ms).
- **Validation:** zero HTTP errors; 443,811 acknowledged writes verified in messages and
  FTS; every Cable message reached every client.

## Memory and latency

These two gaps remain. The analysis is in [CHANGES.md](CHANGES.md).

- **Memory with 1,000 Cable clients:** 113 MB for Go and 58 MB for Rust, about 73 vs 18 KB
  per client. Go keeps a reader and a writer goroutine per connection (24 MB of stacks) and
  net/http's 8 KB of buffers per connection. After the HTTP workloads Go uses 136 MB vs
  103 MB, mostly GC headroom.
- **p99 latency at 16 clients** is about 2.3× Rust's, while Go's median is lower. It isn't
  GC (unchanged with `GOGC=off`) or cgo (worse with more Ps). It matches Rust at 1 client
  and grows with queueing on the saturated CPUs.

## Method and limits

The environment, fairness measures and limitations are those of the
[previous report](../concurrency-20261004/README.md#fairness-and-method). Only the
harness report is committed; per-run samples, logs and profiles were left out. The room,
history, sidebar and search responses stayed byte-identical to #3's, with and without the
fragment cache, after every change.
