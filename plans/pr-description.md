<!-- PR body for basecamp/once-campfire-go. Numbers: matched shared-harness run
     2026-10-14 against the latest published Rust revision f9dca47. -->

# Performance engine and durability fixes

This branch adds a measured performance engine on top of the port — owned
HTTP/1.1 loop, direct SQLite read/write lanes, generation-checked
whole-response cache, batched write path with a WAL restart threshold, cable wake path,
compiled renderer — with the same schema, storage keys, password hashes and
Rails-signed sessions. Every fast path has a rollback switch.

## Results — Go `1b143bf` vs Rust (matched run)

Shared verification harness (`basecamp/once-campfire-verification`, revision
`ec02deb`), Rust at its latest published revision `f9dca47` (the response-cache
revision), both runtime databases on tmpfs, c=16, 3 alternating rounds,
8-second samples, every response contract-checked, zero errors, 660,976
acknowledged writes verified (image `once-campfire-go:final2`, built from the
reported measured revision). The branch also contains later Copilot review fixes
that were not rebenchmarked; these are the earlier reported measurements:

| HTTP workload (requests/sec) | Rust `f9dca47` | Go `1b143bf` | vs Rust |
|---|---:|---:|---:|
| Room page | 109,468 | **148,605** | **1.36×** |
| Messages page | 104,716 | **194,387** | **1.86×** |
| Sidebar | 124,164 | **162,273** | **1.31×** |
| Search | 126,215 | **201,560** | **1.60×** |
| Post a message | 8,177 | **15,392** | **1.88×** |

The local Rust image reproduces the published Rust figures within ~2–4%
(published: 106,494 / 102,697 / 120,294 / 121,378 / 8,037), so the comparison
is against a validated baseline. Local-only raw receipts:
`/tmp/opencode/matched-final/summary.json` (also recorded in
[`plans/validation.md`](validation.md)). See the
[evidence notes](../bench/RESULTS.md#local-evidence-and-historical-reports)
for receipt provenance limitations.

### vs the C port (native, same pinning)

| Route | C | Go `1b143bf` | vs C |
|---|---:|---:|---:|
| Room page | 156,727 | 147,748 | 0.94× |
| Messages page | 173,380 | **197,124** | **1.14×** |
| Sidebar | 168,853 | 163,809 | 0.97× |
| Search | 170,000 | **206,442** | **1.21×** |
| Post a message | 7,456 | **17,073** | **2.29×** |

Ahead of C on messages, search and post; C is faster on room and sidebar by median.

## How

- **Routing**: the compiled production recognizer runs inside `web.Server`,
  controlled by `CAMPFIRE_COMPILED_ROUTES`. The outer `CAMPFIRE_ENGINE` ownership
  scaffold intentionally registers no production routes; it is not the rollback
  switch for the optimized handlers.
- **Owned HTTP/1.1 loop** (`internal/fastserve`, `CAMPFIRE_SERVER_LOOP=on`,
  default): vectorized request parsing, canonical-header fast path, precomposed
  response heads, one writev per response flush, deadline calls eliminated where the
  socket state already matches.
- **Whole-response cache** (`CAMPFIRE_RESPONSE_CACHE_MB`, 64 MiB default) for
  room, messages and search: completed responses stored as their exact wire
  bytes; hits re-check the session and the observed SQLite generation before
  serving; direct replay through the precomposed seam removes the header-map
  and body-copy work. Sidebar keeps its own whole-page cache.
- **Direct SQLite lanes** (`internal/fastdb`, CGO): read lane with fused
  single-transition version and session reads; group-commit write lane with
  FTS, unread state and push intent in the primary transaction, and checkpoints
  off the writer. Push recipient progress and outbox acknowledgements add one
  short transaction per bounded scan, outside network delivery.
- **WAL restart threshold** (`CAMPFIRE_CHECKPOINT_PAGES`, 10,000 frames): a C WAL hook
  tracks frames; the writer runs PASSIVE + RESTART between jobs, so a busy post
  workload holds the WAL at ~40 MB instead of growing unbounded (measured
  4.6 GB in 10 s without it). Refused restarts are counted, backed off and
  never misreported.
- **Cable wake path** (`CAMPFIRE_CABLE_FAST=on`, default): subscriber index,
  exact-payload frame cache, per-client ring with a condition-variable writer,
  batched vectored writes; the unread bump uses an active-subscription index
  and one bulk authorization, with removed members skipped and revoked
  sessions stopped.
- **Runtime**: PGO build (`cmd/campfire/default.pgo`, `-pgo=auto`, falls back
  cleanly when absent; benefit undetected, mechanism only) and `CAMPFIRE_GOGC=200` as the Docker-image default only
  (rollback `CAMPFIRE_GOGC=100`); measured GC CPU per request halves, total
  CPU per request −4.9% on the read workload for +13 MiB peak RSS.

## Correctness

- Full `-race` suite on the final tree (exit 0, including the nested WebSocket
  suite); browser workflows pass; the final screen inventory reproduces the
  reference parity state exactly — **178/178 applicable pixels and
  accessibility**, 0 capture errors, and the same documented strict-DOM and
  network deltas as the reference runs (server DOM 19/192, live DOM 8/178,
  network 0/178, cable 177/178).
- Conditional responses: weak entity-tag comparison and 304 validator
  preservation verified byte-for-byte against `net/http` semantics; an
  independent conformance differential on the final tree reports no
  divergences outside the documented transport deltas (strict duplicate
  Content-Length/Transfer-Encoding handling).
- Go↔Rust interoperability: Rust-issued sessions accepted by Go and vice
  versa; Rust renders and searches messages Go wrote (FTS).
- HTTP response, page, fragment, reference-window and four read caches carry
  external SQLite commits observed by `PRAGMA data_version` on a pinned reader.
  Admission retains the pre-read generation; membership checks stay fresh.
  Pagination ETags derive from the complete rendered representation, including
  related-user changes, rather than message IDs and timestamps.
  Cable publication authorization also gates its memo on the pinned-reader
  generation; the idle heartbeat also observes the pinned-reader generation.
- The write path keeps primary-commit-before-callback semantics, per-job
  savepoint isolation, cancellation survival and a shutdown drain; WAL/NORMAL
  durability is unchanged.

## Measurement notes and limitations

- Warm, single-viewer, tmpfs databases, c=16; three rounds are limited replication.
- WAL threshold is not a hard bound: persistent readers can prevent restart.
  Threshold PASSIVE/RESTART can delay queued writers before accepting the next batch.
- FTS indexing, unread state and push intent commit with the message. Push
  delivery is at-least-once: a crash after remote acceptance but before the
  scan's progress commit can repeat delivery. Acknowledged recipients are not
  replayed; current eligibility is rechecked before sending.
- WAL `synchronous=NORMAL` can lose commits since the last checkpoint on power loss.
- PGO benefit was undetected; GOGC=200 trades memory for CPU and is a Docker-image default only.
- Screen/accessibility parity is not full DOM or wire parity. Historical browser
  results above are not a rerun against these final cache fixes.

- The first run of this comparison accidentally used Rust's pre-response-cache
  revision (`743aa84`, whose numbers match the *old* published table within
  2–3%); this body reports the corrected matched run against `f9dca47`.
- Auxiliary rows (avatar, static CSS, `/up`) and the cable fan-out were not
  re-measured in the matched session; the earlier measurements of those routes
  are recorded in `plans/validation.md` and are not part of the headline claim.
- Known deliberate differences from Rust (strict DOM/network specs, cache
  coherency scope, formatting) are listed under "Known differences" in the
  README.
- `github.com/sebishogun/simdhttp` and `github.com/sebishogun/simd` are
  MIT-licensed modules by this PR's author used only by the owned loop.
