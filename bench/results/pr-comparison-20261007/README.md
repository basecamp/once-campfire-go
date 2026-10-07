# Competing Go PRs and guarded message rendering

The guarded renderer adapted from Nick Potts' [PR #6](https://github.com/basecamp/once-campfire-go/pull/6) is retained. Against our preceding build, it roughly doubles fragment-disabled read throughput and improves posts, with exact complete Go response bytes. Warm read capacity remains mixed. No database, checkpoint, authorization, compression-library or dependency changes were imported from competing PRs.

These measurements use the **local ARM64 OrbStack Linux VM on an Apple M3 Max**, not the native Intel desktop. The desktop became unavailable before its warm follow-up could be retrieved. This report does not combine those populations or use unpublished Intel results to support its claims.

## Method and source identity

Go 1.27.1, Rust 1.98.0 and libvips 8.16.1 in the same recorded builder; four application workers on guest CPUs 0–3, generator on 4–7. Guest affinity is not dedicated physical-core affinity. Public HTTP/1.1, gzip, fresh copies of the pinned 169-message parity seed, three rotating/alternating repetitions, 10-second samples after two-second warmups. Capacity runs use 16 closed-loop clients; paced HTTP uses a separate fixed-rate generator with the same 16-request concurrency bound. No agents, builds or tests ran during application timing. Logs were gzip-compressed at level 9 after each completed phase.

All responses are consumed completely. Preflight checks cover decoded identity/gzip equality, ordered/unique message IDs and search counts. Every acknowledged HTTP, concurrent and Cable write is checked in messages and FTS; Cable requires all subscriptions and complete delivery. Only the original render-time room cursor is explicitly masked for cross-PR preflight comparisons. Our before/after cold and warm tests require exact complete decoded bytes after replacing only their known listener origins, without that mask. Sidebar is excluded because its full-layout work is not equivalent across ports.

[Manifest](manifest.json.gz) records per-variant source/generated-asset hashes, binary hashes, dependency/build information and PGO profiles. [Harness/build record](harness.json), [commands](checks/run.sh), [builder](checks/Compare.Dockerfile), and [gate logs](checks/) retain the setup. Source hashes, rather than the root checkout's dirty Git identifier during staging, identify the actual binaries. Our renderer is commit `1338bbf`; the preceding production code is `7afad06` (archived at `85c302f`). Rust is pinned to `64f86353021145b63849fb1cd93adeb08f3b8dbb`.

| Variant | Exact head | Runtime/build difference |
|---|---|---|
| [PR #2](https://github.com/basecamp/once-campfire-go/pull/2) | `8a374d7600cc37500509730a3d62fb9103315ee4` | Docker sets `GCGO=200`, not `GOGC`; no application-code optimization |
| [PR #4](https://github.com/basecamp/once-campfire-go/pull/4) | `3b2054f9dac4b50c5bc1a6a29d8cd05992133034` | Automatic checked-in PGO; writer/checkpointer/renderer changes |
| [PR #5](https://github.com/basecamp/once-campfire-go/pull/5) | `f2c50e0fcf3efa1454bedb2741c3c54e0374f6f7` | Process-local content-generation caches and compressed pieces |
| [PR #6](https://github.com/basecamp/once-campfire-go/pull/6) | `99138d721101b43180bf4541af0aee1c6645bdad` | Template slots, database snapshots/checkpoints, reaction-form changes |
| [PR #7](https://github.com/basecamp/once-campfire-go/pull/7) | `18e3b236b9c7f535b379d58d799cc8c77d54d4fb` | Independent deflate blocks/CRC composition |
| [PR #8](https://github.com/basecamp/once-campfire-go/pull/8) | `0f7e0e0e68a8a4f1f0eab9b23cee01d39dc1761d` | Automatic PGO, generated views, direct SQLite; **startup sets GOGC=200** when unset |

The main tables measure PR defaults: GOGC is unset, yielding 100 except PR #8's explicit startup override. A separate PR #8 follow-up sets GOGC=100 for all Go participants. PR #8 also omits the `sqlite_fts5` build tag because its vendored adapter enables FTS5 itself. SQLite versions/adapters differ; this is an implementation comparison, not a controlled SQLite-library experiment.

## Capacity: warm reads and writes

Median successful requests/sec; each cell has three samples. Latency at saturation is **not** latency at equal offered load. See [full ranges, CPU, tails and memory](competitors-capacity/report.md).

| App | Room | History | Search | Posts | End-suite PSS MiB |
|---|---:|---:|---:|---:|---:|
| Our candidate | 20,626 | 20,762 | 19,094 | 2,414 | 141.7 |
| PR #2 | 4,122 | 5,179 | 6,834 | 2,217 | 143.8 |
| PR #4 | 4,636 | 5,585 | 9,902 | 3,564 | 142.3 |
| PR #5 | 1,005 | 5,219 | 46,396 | 2,440 | 140.7 |
| PR #6 | 5,622 | 7,473 | 12,031 | 2,650 | 134.8 |
| PR #7 | 14,930 | 16,104 | 10,256 | 2,224 | 140.9 |
| PR #8, GOGC=200 | 4,224 | 5,019 | 9,086 | 2,792 | 175.8 |
| Rust | 27,491 | 28,753 | 23,862 | 3,033 | 125.8 |

Our candidate leads these Go variants on room/history, but not search or posts. Rust remains ahead on this VM. In the [GOGC=100 follow-up](pr8-gc100/report.md), PR #8 measures 4,227/5,078/9,427/2,834 requests/sec and 129.1 MiB end PSS; our candidate measures 21,766/21,585/20,814/2,508 and 139.9 MiB. These repetitions are a separate comparison, not a causal estimate of changing GOGC alone.

## Fragment retention disabled

Same three-repetition, gzip/16-client protocol with `CAMPFIRE_FRAGMENT_CACHE_MB=0`. This disables fragment retention, not every variant's other caches. [Raw comparison](competitors-cold/report.md).

| App | Room req/s | History req/s | Search req/s |
|---|---:|---:|---:|
| Our candidate | 976 | 1,065 | 2,336 |
| PR #2 | 277 | 300 | 773 |
| PR #4 | 738 | 719 | 1,817 |
| PR #5 | 140 | 192 | 315 |
| PR #6 | 478 | 542 | 1,541 |
| PR #7 | 317 | 318 | 839 |
| PR #8 | 787 | 880 | 2,289 |
| Rust | 1,423 | 1,483 | 4,238 |

## Equal offered rates

The port-owned `bench/httprate` schedules arrivals independently of completions. Latency begins at each scheduled arrival, including generator lateness and bounded client queueing; drops, expiry, errors and drain time are exposed. It fully consumes encoded bodies, rejects non-200/empty/truncated responses and never follows redirects. Its regression checks cover backlog/coordinated omission, cancellation/overflow, response completion, POST contracts and quantile bounds.

Median scheduled p99 milliseconds at the same offered rate:

| App | Room: 500/s | Search: 5,000/s | Posts: 1,000/s |
|---|---:|---:|---:|
| Our candidate | 10.576 | 5.688 | 9.088 |
| PR #2 | 8.608 | 678.912 | 9.760 |
| PR #4 | 8.448 | 9.312 | 7.336 |
| PR #5 | 20.032 | 6.648 | 8.512 |
| PR #6 | 9.328 | 7.120 | 7.512 |
| PR #7 | 8.720 | 13.008 | 9.104 |
| PR #8 | 7.312 | 7.552 | 7.168 |
| Rust | 7.504 | 5.016 | 7.768 |

**Generator/VM scheduling is a material limitation:** median generator p99 lateness is about 3–5 ms. These are not server-only tails or native Intel latency results. PR #2's search median includes substantial client backlog (queue p99 about 675 ms); that offered load is not demonstrated as a steady-state operating point for it with this generator. No samples dropped or expired arrivals, but zero errors alone does not establish steady state. See separate [room](competitors-rate-room/report.md), [search](competitors-rate-search/report.md) and [post](competitors-rate-posts/report.md) tables for p50/p95, service/queue/generator components and raw counts. Our p99 targets relative to Rust are not met here; they were aspirations, not grounds to weaken behavior.

## Retained renderer, isolated against our preceding build

Four no-boost emoji/attachment layouts are compiled from the **existing templates**, guarded by the exact source hash. Boosts or unsupported source changes use normal `html/template`. Ordinary strings preserve its exact escaping, including plus and NUL; attachment download URLs still use its URL filtering/normalization. All eight reaction forms, trusted HTML boundaries, hydration, freshness and immutable response ownership remain unchanged. There is no global avatar cache or new dependency.

| Workload | Before req/s | Candidate req/s | Before / candidate p99 ms |
|---|---:|---:|---:|
| Cold room | 460 | 931 | 87.935 / 40.415 |
| Cold history | 467 | 1,026 | 88.511 / 33.759 |
| Cold search | 1,064 | 2,196 | 47.423 / 20.559 |
| Cold posts | 1,997 | 2,267 | 32.607 / 29.343 |
| Warm room | 20,429 | 20,010 | 5.043 / 5.047 |
| Warm history | 20,251 | 19,792 | 5.167 / 5.287 |
| Warm search | 18,814 | 18,961 | 4.475 / 4.427 |
| Warm posts | 2,265 | 2,605 | 29.007 / 25.423 |
| Active room, 20 writes/s | 29,865 | 29,505 | 2.281 / 2.371 |
| Active search, 20 writes/s | 14,503 | 14,996 | 5.019 / 5.027 |

[Cold](slots-cold/report.md), [warm](slots-warm/report.md) and [active](slots-active/report.md) trials retain complete-byte, fresh-content and messages/FTS checks. Cold read CPU/request falls about 58–64%. Warm room/history capacity falls about 2%; no general warm-read speedup is claimed. End-suite PSS is before/candidate 71.6/67.6 MiB cold, 140.2/140.8 MiB warm and 146.7/147.3 MiB active. These workload high-water values are not a universal memory guarantee.

At 1,000 Cable clients, [matched paced-only delivery](slots-paced-cable/report.md) sends exactly 30 messages per encoding and no throughput burst. Before/candidate end PSS is 140.5/141.2 MiB; plain all-client p99 47.327/48.991 ms, deflate 49.279/54.207 ms. This supports a roughly unchanged footprint for that matched population, not 10,000 clients.

The separate [saturated Cable trial](slots-cable/report.md) is mixed: median complete fan-out 326.8→257.1 messages/s plain and 227.2→225.0 deflate, with broad plain ranges (213.6–334.7 before; 235.5–329.5 candidate). Paced tails in that mixed suite improve plain and stay unchanged deflate; saturated tails fall. End PSS rises 223.6→232.2 MiB after unequal HTTP/Cable write volumes. Do not hide the lower plain median, compare these high-water values as equal populations, or claim a Cable capacity win. Transport batching was reviewed but not imported without an isolated demonstration against our indexed hub.

## What was not imported

- PR #4/#8 writer paths deliberately detach cancellation; PR #8 also changes transaction membership/error/cleanup boundaries. Faster post medians are not evidence that those altered contracts or checkpoint synchronization timings are interchangeable with ours.
- PR #5's search key uses the process-local `ContentGeneration()` counter (`internal/database/recent.go`, `internal/web/server.go`). Commits from another connection/process do not bump it. Its 46k warm-search result therefore does not justify replacing our fresh SQLite reference observation under the external-commit visibility contract.
- PR #6's one reaction form replaces the eight separate forms; its direct string escaping misses plus. We retained neither change. Only the source-derived layout algorithm was adapted, with escaping and activation/fallback differential tests.
- PR #8's generated views, SQLite adapter and Cable lifecycle require broader escaping, ownership and fresh-authorization repairs. In particular, subscribe-time authorization is not our publication-local fresh session/membership check, and its clipboard URL-value attribute is not the Rails frontend's working content-value contract.
- PR #7's compression approach was measured as a complete PR, not adopted merely from compression-only ratios. Our bounded completed-response gzip memo remains; no new compression dependency was justified.

All six PRs and the retained code passed local Linux vet/race and WebSocket-fork gates. The renderer also passed whole-byte branch/hostile URL/text/date tests and 311,322 escaping fuzz executions. The complete set contains **159 application runs, 348 HTTP samples and 24 Cable samples**, with zero HTTP errors and complete Cable delivery. All acknowledged writes, active mutations and Cable posts were verified in messages/FTS; exact counts remain in each raw report.

The seed is small, trials are short and source/frontend/SQLite/runtime differences remain explicit. There is no fresh Ruby/Elixir, TLS, native cross-PR, long-tail large-seed or cross-port Cable result here. The report is evidence for these changes and observed implementations, not language-wide parity, an absolute ceiling, or a claim to be fastest everywhere.

## Reproduce

Use the pinned reference parity seed, recorded toolchain, exact PR refs and per-source manifests. Build each archive with `bin/build-assets`, native CGO and its recorded tags/automatic PGO; build `bench/httprate` normally. `checks/run.sh` gives each phase's binary mapping, affinity, rates, repetitions and validation settings. Replace only its scratch paths with your corresponding binaries; do not omit the full response/persistence checks.

Aggregate raw samples, metadata and source manifests are gzip-compressed here to keep the review compact. Decompress a phase's `raw.json.gz` and `metadata.json.gz` before rerunning `bench/report`. Reports and reproducible helpers are included. Full compressed server/generator logs and decoded initial HTML are retained in the [complete fork archive](https://github.com/nijaru/once-campfire-go/tree/perf/prepared-gzip/bench/results/pr-comparison-20261007) instead of copying historical experiment gigabytes.
