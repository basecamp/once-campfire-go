# Engine design — once-campfire-go performance fork

Date: 2026-10-05
Status: approved for implementation
Goal: beat the Rust port (basecamp/once-campfire-rust) on the ecosystem's
official production-image benchmark on this machine — median at 16 concurrent
clients on room page, messages page, sidebar, search and post message, plus
cable throughput/latency — by as much as the performance-aware tenets allow,
with every existing correctness/parity validation still green.

## 0. Acceptance

The win is measured, not asserted:

- **Official harness**: `../once-campfire-elixir/bench` running production
  Docker images (Rails reference, our Go image, the Rust image), fresh copies
  of the same seeded SQLite fixture, four application CPUs (8–11), load
  generator on 12–15, host network, load average < 1 before each rep, rounds
  alternating app order. Median of 3 repetitions per row; ranges retained.
- **Every row**: Go median ≥ Rust median at c=16 on `room_show`,
  `messages_page`, `sidebar`, `search`, `post_message`; cable delivery
  throughput and all-clients p99 no worse at 100 and 1000 clients.
- **Secondary**: c=1 and c=64 reported; memory and cold start must not regress
  against the current published Go image.
- **Validation**: the full suite from `plans/validation.md` (race tests,
  golden vectors, browser workflows, screen inventory, cookie/schema upgrade
  interoperability, official harness write/search/delivery counters) passes
  with the engine enabled. Strict pre-existing DOM/network differences remain
  documented; no new allowlists.

## 1. Tenets

The performance-aware block in `CLAUDE.md` applies in full. In this project it
maps to:

1. **Zero allocations per request.** Engine request path targets
   `-benchmem`-verified 0 allocs: borrowed request buffers, per-connection
   scratch, cached immutable pieces, no map/sprintf/decode per element.
2. **Data layout first.** Responses are ordered *pieces* (immutable
   `[]byte` + precomputed member bytes/digests), not re-rendered HTML; cache
   entries are sized and content-addressed; hot fields adjacent.
3. **Bulk/SIMD where measured.** `simdhttp` parse/router (measured 236 ns/0
   allocs vs net/http 1,260 ns/16 allocs on this box), `simd` text kernels,
   `simdjson` for Cable/Turbo JSON — each adopted only after an in-app A/B.
4. **Don't do the work at all.** No per-request template execution, no
   per-request gzip of already-compressed content, no per-request body hashing
   (ETag from cached piece digests), no query a version number can answer.
5. **Shard on existing boundaries.** One writer, reader connections on app
   CPUs; per-connection output buffers; merge once.
6. **Pools last.** Arenas/borrowed buffers before `sync.Pool`; any pool gets a
   poisoning test first.

## 2. Where the current port spends time (evidence)

From the checked-in pprof profiles (`bench/results/optimization-next-20261003`,
`optimization-20261003`) on room page (4 app CPUs, 16 clients):

| Cost | Share | Notes |
|---|---:|---|
| `front.Deflate` (cum) | 81.9% | wraps the handler; `gzipResponse.Write` 20.5% compresses the 374 KB body per request |
| Socket writes (`Syscall6`, flat) | 19.5% | ~4 KB bufio flushes of *uncompressed* bytes |
| `responseBuffer.finish` + marker replace | 22% | `strings.ReplaceAll` over 374 KB, then parts assembly |
| `sha256.blockSHANI` (flat) | 7.7% | per-request part digests |
| DB reads + `cgocall` | ~24% | `MessagePageReferences` 15.2%; `database/sql` row/interface overhead |
| `roomShell` + `messageList` rendering | 11.9% | already cached; not the problem |

Sidebar after caching is DB-bound (`database/sql.Rows.Next` 29.5%, cgocall
23.8%). Post message is `createMessage` 68.9% cum, `messageViews` 39.6%,
template/markup 23.9%, sqlite write ≈15%. Cable deflate at 100 clients is
1,878 msg/s vs Rust 3,462 (same harness).

Rust's advantage is architectural and already mapped: fragments are cached as
deflate pieces; page gzip is assembled from pieces with CRC combination
(`crates/kit/src/deflater/splice.rs`); ETags hash part identity records, never
the body; columns read by position (`db` crate); writer/reader scheduling with
WAL checkpoints off the writer; custom header-first WebSocket reader with
batched `write_vectored` and compressed-once shared frames.

## 3. Architecture

### 3.1 Seam

`main.go` composes the engine around the legacy server:

```
legacy := web.New(...)                 // untouched fallback
root   := engine.New(legacy, db, secrets, cable, storage, engine.Config{...})
front.Serve(ctx, front.FromEnv(), root)
```

- `engine.ServeHTTP` matches its explicit route table (compiled trie; a
  `simdhttp` router is the candidate). Matched routes run the fast pipeline;
  everything else delegates to `legacy.ServeHTTP` with zero inspection beyond
  the match.
- Route ownership is all-or-nothing per route; unknown methods/params on an
  owned path fall back for that request to legacy and count under a
  diagnostic counter (fallback must be zero on the benchmark corpus).
- `CAMPFIRE_ENGINE=off|on|force` controls ownership: `off` serves every route
  through legacy (A/B and rollback), `on` is the default, `force` is for tests.
- HTTP semantics that engine routes still rely on from the existing stack
  (autocert/TLS, HTTP/2, public-front cache for assets, request body limits)
  stay in `internal/front` unchanged.

### 3.2 Encoding composition (seam catch)

`front.Serve` currently wraps its argument in `front.Deflate` (gzip). Engine
responses are already encoded, so a naive insert would double-compress. The
composition moves up one level:

- engine routes: engine chooses encoding itself, sets `Content-Encoding` and
  `Vary`; `front.Deflate` never sees them.
- legacy routes: keep exactly the current `bodyLimit(Deflate(...))` chain.
- `PublicCompression` already passes through responses that carry
  `Content-Encoding`, so the public listener behavior is unchanged (zstd
  jitter path still applies only to unencoded bodies).

Implementation detail: `front.Serve` gains an encoding-aware wrapper around
the handler (smallest possible change to `internal/front`), or an exported
`front.ServeApp` that takes pre-composed chains; the plan picks one with the
existing front tests as the regression net.

### 3.3 Packages

| Package | Responsibility |
|---|---|
| `internal/engine` | route table + match, fast auth/session, response writer (encoding, assembler, validators, 304), error mapping, counters |
| `internal/piececache` | content-versioned compressed pieces: member bytes, raw bytes, length, CRC, digest; byte-bounded LRU with the existing eviction policy; atomic replace |
| `internal/fastrender` | compiled byte renderers for dynamic pieces (message fragment, `loadedAt`, stream tokens, flash, sidebar rows); golden-tested against legacy templates |
| `internal/fastdb` | thin CGO SQLite: per-connection statement cache, positional column reads, typed row decode into caller-owned structs; same DSN/pragmas as `internal/database` |
| `internal/netjson` | (phase 2) simdjson-backed Turbo/Cable frame encode/parse with a stdlib fallback; adopted only if measured |

The engine never imports `internal/web` internals; it uses exported
`database.DB`, `rails.Secrets`, `cable.Hub`, `storage.Store`, `richtext.*`,
and the legacy handler only as fallback.

### 3.4 Response model

A response is an ordered list of *pieces*. Each piece is one of:

- **cached member piece**: an immutable gzip member (own header/CRC/ISIZE) and
  its raw bytes and digest, already in memory;
- **cached raw piece**: raw bytes only (used for identity encoding);
- **dynamic piece**: small per-request bytes (e.g. `loadedAt`), compressed on
  the fly only when gzip is required.

Assembly for gzip is the concatenation of member bytes — every client
implementation, `net/http`'s own `gzip.Reader` included, decodes
multi-member gzip by concatenation (RFC 1952 §2.2), so no raw-deflate window
bookkeeping or CRC combining is needed. Identity assembly is raw-piece
concatenation. Both go into a single output buffer and one `Write`.

This is deliberately coarser than Rust's per-fragment raw-deflate splice: three
or so member boundaries per page instead of per-fragment back-reference
optimization. The tradeoff is +a few KB per response against a much simpler,
allocation-free assembler; §6 measures response bytes, and if size costs
throughput the finer variant is a plan item, not an assumption.

### 3.5 Version registry

Piece keys are content versions, and the request path must decide "hit" with
the cheapest query that is still correct. The engine keeps an in-process
registry of versions, seeded from the database on first use:

- `room.MessageVersion` bumped by every in-process message create/edit/delete
  (the same code paths that touch `rooms.updated_at`);
- `room.MembershipVersion`, `user.SessionVersion`, `user.StatusVersion`,
  bumped by in-process membership/session/ban writes;
- `account.SettingsVersion` for account-level page data.

Writes go through the legacy code paths (engine routes share the same
`database.DB` and job code), so the bumps hook the existing write helpers.
The single-process assumption matches the Rust port's own fragment cache and
is documented as a deliberate limit: changes written by another process
(e.g. a restored backup) are picked up on restart, not live.

## 4. Data flow

### 4.1 Room page, cache hit (the headline path)

1. Engine reads the request head into borrowed buffers (net/http initially;
   simdhttp if its phase-2 A/B wins).
2. Cookie decrypt (existing `rails.Secrets`), session resolved from a
   version-keyed authentication cache; only a miss touches `fastdb`.
3. Room access resolved from membership cache; room/message versions consulted
   to build `(shellKey, messageListKey)`.
4. `piececache` probes both keys; hit path assembles members + the small
   `loadedAt` member into the output buffer; ETag computed from the pieces'
   precomputed digests; conditional GET returns 304 without assembly.
5. One `Write`; browser-session cookie committed only if changed.

Expected per request: two cache probes, O(3) slice copies over compressed
bytes (~60–90 KB), one digest combine, one write syscall, 0 allocations.

### 4.2 Room page, miss

`fastdb` reads the minimum (page references, room, membership, invitation)
positionally; `fastrender` executes the compiled shell/message renderers only
for the missing piece; the piece is stored with its version key; assembly
proceeds as above. Rendering correctness is gated by `fastrender`'s golden
tests; the steady-state benchmark is hit-dominated.

### 4.3 Other routes

- **Sidebar**: key from user/room/membership versions. Hit: probe + assemble.
  Miss: one `fastdb` join + compiled renderer. Removes the
  `Rows.Next`/`cgocall` dominance.
- **Messages page**: same shape; ETag derived from the cached piece digests of
  the message page references (precomputed), 304 before any assembly.
- **Search**: `fastdb` FTS query with positional reads; rendered result pieces
  cached under `(query, corpusVersion)` with a bounded LRU; corpus version is
  bumped by message writes. Rust recomputes FTS per request — this is a
  measured "don't do the work" candidate, reverted if the corpus versioning
  shows holes in differential fuzzing.
- **Post message**: `fastdb` write (single immediate transaction as today),
  message fragment rendered once through `fastrender` and shared by the Turbo
  response and the cable publish; push/webhook/FTS enqueue unchanged.
- **Cable**: reuse the existing hub first (shared prepared frames already
  exist). Phase 2, only if measured behind: batched writes, authorization
  cached on the same version registry, ring buffers instead of channels.

## 5. Correctness

- **Differential oracle.** Same request, same frozen clock
  (`CAMPFIRE_FROZEN_TIME`), same database copy, once with
  `CAMPFIRE_ENGINE=off` and once `force`; byte-compare after masking only
  fields the legacy path itself varies per request (documented list). A
  corpus covers hit and miss, 200/204/304/404/403/422, invalid params,
  format rewrites, direct rooms, closed rooms, admin users, bot UA.
- **Cache poisoning tests first.** For every cache, a test fills it, mutates
  the underlying row through the normal write path, then asserts the next
  request equals the legacy response — never the stale piece. Fuzz sequences
  of mutations and reads under `-race`.
- **Version registry audit.** Every write helper that bumps a version is
  covered by a test that asserts the corresponding piece key changes.
- **Route ownership gate.** A test asserts the engine route table is a subset
  of the legacy contract table, and that `CAMPFIRE_ENGINE=on` fallback
  counters are zero for the benchmark routes on the seed fixture.
- **Existing gates stay**: `go test ./...`, `go test -race ./...`, `go vet`,
  `gofmt -l .`, golden richtext/user-agent/cookie vectors, browser workflows,
  screen inventory, upgrade interoperability.

## 6. Measurement contract

- **Inner loop**: `bench/application` native binaries, alternating
  go-engine/go-legacy/rust order, 3 repetitions, minimum reported alongside
  median; CPUs 8–11 / 12–15; the seed copied per rep; identity and gzip rows.
- **Public claim**: official Elixir harness with production images; raw
  samples, metadata, image ids and load averages retained under `tmp/bench/`
  and summarized in `plans/` (no tracked benchmark output).
- **Below wall-clock resolution**: `perf stat -e instructions:u,cycles:u`;
  allocation profiles with `-benchmem`; `go tool objdump` before theorising.
- **Hygiene**: quiet machine (load < 1) for published numbers; no concurrent
  builds; every build/test command carries an explicit timeout; gates run
  bare (or with `pipefail`); a measurement that argues against a change goes
  to `plans/wrong.md`.
- **Everything that can be measured is measured**: per-route req/s and latency
  percentiles, CPU µs/success, response bytes (raw and encoded), allocations
  (`-benchmem` on hot paths), memory/startup, cable delivery throughput and
  per-client latency, write persistence and FTS verification counters.

## 7. Milestones

| ID | Task | Exit |
|---|---|---|
| M0 | Environment and baseline reproduction | fork builds with engine flag placeholder; seed + loadgen + images reproducible; both harnesses reproduce published Go/Rust numbers on this box |
| M1 | Engine seam, flag, counters, differential harness | engine serves zero routes; tests prove fallback parity and encoding composition |
| M2 | Room page | differential green; §0 acceptance row beaten vs Rust on inner loop, then confirmed on official harness |
| M3 | Sidebar + messages page | rows beaten; DB-bound costs reduced with positional reads |
| M4 | Search + post message | rows beaten; write persistence counters green |
| M5 | Cable | throughput and p99 no worse than Rust at 100/1000 clients |
| M6 | Official head-to-head + records | §0 acceptance; README/plans updated; upstream PR prepared |

## 8. Risks and mitigations

- **Parity drift** in HTML/headers → differential oracle plus legacy fallback
  per request; no route ships without its differential corpus.
- **Cache correctness** (stale pieces, version holes) → poisoning tests first;
  version registry audit; single-process limit documented.
- **Thin CGO layer bugs** (statements, types, busy handling) → differential
  tests against `database/sql` reads on the same fixture; writes can stay on
  `database/sql` until reads prove out.
- **Benchmark comparability criticism** → run the ecosystem's official
  harness, unmodified images built from this tree, raw artifacts published
  with metadata; keep legacy mode in the same binary so the delta is
  attributable to the engine alone.
- **Cable rewrite scope** → only after HTTP rows win; authorization cache
  changes get the same poisoning/fuzz discipline.
- **Time/context** → route-by-route deliveries with measurable exits; each
  milestone's evidence recorded before the next begins.

## 9. Non-goals

- Replacing the public front server, TLS/ACME, HTTP/2, or the public response
  cache for assets.
- Rewriting media, push, webhooks, jobs, admin, or storage paths.
- Multi-process/clustered cache coherence.
- A custom socket loop in phases M1–M5; it is reserved (measured-gated) for a
  phase M6+ only if `perf stat` still shows transport-dominated cost after the
  engine path is otherwise saturated.
