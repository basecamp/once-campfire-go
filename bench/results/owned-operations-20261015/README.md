# Owned operations: performance and verification

This change separates HTTP adaptation, application operations, database commands and presentation while preserving existing SQLite data, storage, cookies and frontend contracts. The runtime source under `cmd/`, `internal/`, `assets/`, `third_party/` and the Go module files matches the tested implementation `8351224` exactly. A clean-branch rebuild also matches the previously verified Mac executable byte-for-byte.

The result is not uniformly faster than upstream. It improves measured POST capacity and live throughput over upstream Go, while fixed-load POST latency still loses to Go and Rust and live throughput still loses to Rust.

## Matched native comparison

On 2026-10-13, exact implementation `8351224b56c9a3c6784ed5d7510857212133ac4c`, upstream Go `c4ab53d00b0a19a55962e03d18057ca6c3da9437` and Rust `6dae2fd3c1a2dce61ca11beb731bcce2b42b67ad` ran on one physical Fedora i9-13900KF desktop, Linux 7.2.9-201.fc44.x86_64, 32 logical CPUs, without virtualization. Both upstream main HEADs were rechecked unchanged on 2026-10-15. This is source continuity, not a new benchmark on the desktop's subsequently updated kernel.

Go 1.27.1/CGO/FTS5, four workers/readers, three jobs, default GC, gzip and the same fresh seed/providers were used. Server CPUs were 0,2,4,6; clients 8,10,12,14; runner 16,18. Affinity is not exclusive-core or thermal isolation. There were no task-owned builds, tests, agents, profilers or process samples during timing.

Exactly 27 runs used three balanced orders per workload: upstream/implementation/Rust, Rust/upstream/implementation, implementation/Rust/upstream. All valid adverse runs are retained in [`results.json`](results.json); no favorable-run selection or retries enter the medians.

| Median of three rounds | Implementation | Upstream Go | Rust |
|---|---:|---:|---:|
| POST p99 at 3,000 offered/s, ms | 0.965 | 0.831 | 0.635 |
| POST capacity, requests/s | 9,765.5 | 9,094.9 | 7,155.7 |
| Saturated POST p99, ms | 6.199 | 6.519 | 3.479 |
| Complete live delivery, messages/s | 3,087.8 | 2,364.3 | 4,351.9 |
| Paced all-client delivery p99, ms | 6.507 | 7.311 | 4.951 |

- Fixed POST: 3,000/s, 64 workers, bounded queue 8192, active pacing, two warmup plus eight measured seconds, gzip.
- Capacity: two warmup seconds with four clients, then eight measured seconds with sixteen, gzip.
- Cable: 64 distinct authenticated people with six subscriptions each, 300 paced messages at 100ms intervals, then five saturated seconds with four posters.

All arms use matching maintained clients and complete-wire responses. POST receipts verify content type, gzip integrity, complete Turbo output, request/client sequence and positive message identity. Latency ends before receipt validation; validation and retention add client work and are not assumed neutral.

All **1,190,787 writes** pass applicable exact audits: 270,000 fixed, 772,202 capacity and 148,585 Cable. These cover receipt-to-persistence IDs, bodies, client identity, creator/room and FTS; Cable also covers complete delivery to all clients and presence cleanup. Integrity passes and the seed's existing single FK orphan is unchanged. These checks do not certify crash durability or external provider delivery.

POST capacity is +7.4% versus Go/+36.5% versus Rust. Live throughput is +30.6% versus Go but −29.0% versus Rust. Fixed p99 is +16.1%/+52.0% and loses every round. Three rounds show observed repeatability, not precise confidence bounds. Capacity latencies compare different achieved loads. Read-route rankings are not refreshed.

The [complete immutable evidence archive](https://github.com/nijaru/once-campfire-go/tree/d287f419d052a196c52497acb2c06fca85742f27/bench/results/current-three-way-20261013) retains full samples, exact ACK sidecars, hashes, capture adapters, source/client/seed/image identities and all adverse outcomes. This branch carries the compact round summaries and archive-manifest hash rather than copying the entire experiment history. Cookies, databases, runtime logs and raw execution/perf traces are not included here.

## Correctness and regression protection

The implementation fixes demonstrated repeated-presence unread accounting, disconnect-abandoned attachment processing and a shared unread/push timeout budget. Focused regressions protect those scenarios; existing tests also cover writer-current permissions, canonical body/FTS updates, snapshot freshness, bounded admission, storage continuations and joined shutdown. Bug fixes are not claims of greater throughput.

Verification on 2026-10-14, with clean-branch `bin/check` repeated on 2026-10-15:

- `bin/check` passes formatting, assets, benchmark audit regressions, vet, all application race tests and the nested WebSocket race suite.
- Ten existing non-media HTTP workflows pass against both this Go and current Rust: authentication, message lifecycle, pagination, room membership, retained direct participants, involvement, direct ping, sidebar, room updates and account settings. All twenty runs pass.
- Pristine shared Chromium smoke passes setup, two-tab messaging, duplicate suppression, stored-markup safety, permalinks, editing/search, profile/account updates, QR, room create/rename, bots/styles, session transfers, joining and direct-ping autocomplete through the public listener. The server joins with exit 0.
- The existing upgrade contract, invoked with explicit binary paths, passes Rust→Go and Go→Rust login cookies and Rust rendering/search of a Go-written message/FTS entry in the same database. Seed data is not modified.
- Targeted independent source reviews of database/application and Cable/transport/cache hot paths identify no concrete blocker or material DSA defect. They do not establish universal correctness or optimality.

[`verification.json`](verification.json) records exact check/binary/tool identities and workflow outcomes without request traces or runtime data. The clean-branch Mac binary is `f1470665a951c05298353ea06db8397064ce668f27383d6f0b5ab47ef6b3fdac`, matching the HTTP/interoperability binary. Native browser verification uses exact production binary `8a1409c09aa79e188010fb04dfa0c63b66e4f2748de734a117f6ae5db49b0e29` and pristine shared smoke `38746232a3358bce203e4e6f410338e5f74e5b091f4c9d8d03d0978db184bdfb`.

Media processing is unchanged by the final packaging. Earlier media-output checks remain source-specific evidence; they are not freshly recertified here. Exact DOM/network parity, every platform and live external services are not claimed. The inherited ARM64 assembly-vet warning in the unchanged WebSocket fork is not made green by the application vet gate; its amd64 vet and race checks pass.

## Profiling and rejected alternatives

Prior profiling covers Go CPU/allocations, independent Linux perf, SQL plans/transaction work, writer/queue waits and correlated HTTP/live-frame traces. It supports reducing redundant queries, derivations, ownership copies and permission-map materialization, rather than larger caches or weaker authorization. No generic framework, permission memoization, queue enlargement or production GC tuning is added.

Limitations matter: Go gzip sampling was nonrepresentative in some captures; Linux perf lost upstream samples and had incomplete cgo chains. Component percentages therefore do not explain every competitor gap. End-to-end matched results above are separate uninstrumented evidence.

Failed private controls were not integrated, including a cache-observation guard, association-query consolidation, specialized gzip encoder, cooperative handoff, multi-frame batching, TCP gather writes and disabled SQLite memory accounting. The last flag saved only 0.10% whole-application CPU while losing global memory statistics and SQLite heap-limit enforcement; accounting remains enabled. See the [public profiling/closure archive](https://github.com/nijaru/once-campfire-go/tree/d287f419d052a196c52497acb2c06fca85742f27/bench/results) for retained profiles, finite criteria and rejected outcomes. The branch does not claim those experiments' isolated or Mac-only gains.
