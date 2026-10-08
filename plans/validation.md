# Port validation — 2026-10-02

The full application is implemented. Tests and browser workflows pass; the screenshot and accessibility layers match in the exercised inventory. **Strict all-layer parity is not a pass:** server/live DOM and network differences remain visible.

## Automated checks

| Check | Result |
|---|---|
| Formatting, generated assets, vet and race tests | [Pass](validation/check.txt) |
| 315 frontend asset paths/bodies, importmap and stylesheet tags | [Pass](validation/assets.txt) |
| Cookie interoperability in both directions; Rust reads/searches Go-written messages | [Pass](validation/upgrade.txt) |
| Chromium setup, two-tab messaging, editing, clipboard links, search, profiles/account, rooms, bots, styles, QR, transfers/invitations and direct pings | [Pass](validation/browser.txt) |
| Pinned-toolchain media byte goldens | [Pass](validation/media.txt) |
| Live ACME issuance, HTTPS and cached restart with CA offline | [Pass](validation/acme.txt) |
| Non-root production container, public HTTP, setup, live backup, offline restore and restart | [Pass](validation/container.txt) |

Package tests use fresh temporary databases. Integration tests did not skip for a missing seed. The upgrade and screen runs use disposable copies of the original seed. Media goldens use the pinned container versions; the browser and benchmark use the same native media libraries for both applications. ACME uses a local Pebble CA; Web Push delivery uses local tests rather than external providers.

## Screen inventory

The original Chromium inventory covers 214 desktop/light cells across default, first-run, custom-style, restricted-account and crowded-account seeds. Another 12 phone light/dark cells cover sign-in, account settings, message actions, open/direct rooms and history. No cells errored and no Go-specific masks or allowlists were added.

| Layer | Desktop matches | Desktop differences | Not applicable | Phone matches | Phone differences |
|---|---:|---:|---:|---:|---:|
| pixels | 198 | 0 | 16 | 12 | 0 |
| aria | 198 | 0 | 16 | 12 | 0 |
| server | 21 | 193 | 0 | 0 | 12 |
| live | 9 | 189 | 16 | 0 | 12 |
| network | 0 | 198 | 16 | 0 | 12 |
| cable | 197 | 1 | 16 | 12 | 0 |

Strict whole-cell results: 12 desktop passes, 202 desktop failures. A matching screenshot does not turn a DOM/network failure into a pass. Fragment/protocol-only cases do not have screenshot, live-DOM, accessibility or Cable layers.

The main run was followed by five focused captures after fixing thumbnail URL defaults, sidebar classes and the remote-disconnect reason. The table uses the later results for those five cells. Each retained report has its own binary hashes; this is an aggregate of the full run and focused verification, not a claim that the entire inventory was rerun after the last three fixes.

Raw layer reports: [default](validation/default.json), [first_run](validation/first_run.json), [custom_styles](validation/custom_styles.json), [restricted](validation/restricted.json), [crowd](validation/crowd.json), [phone](validation/phone.json), [phone_chat](validation/phone_chat.json), [cable_fixes](validation/cable_fixes.json). [Aggregate counts](validation/summary.json). Metadata files beside them record seed, frozen clock and Go/Rust binary hashes. Full local HTML reports/screenshots remain under `.cache/screens-*`.

## Differences retained

- HTML differences include empty input values, optional input-size attributes, whitespace, attribute serialization and canonical form-action URLs. See the [sign-in DOM example](validation/sign-in-server.norm.html.diff).
- Network differences include asset `Last-Modified` versus `Accept-Ranges`, response security/transfer headers and HTML-derived validators/body hashes. Assets themselves are byte-identical. See the [sign-in network comparison](validation/sign-in-network.txt.diff).
- The sole remaining desktop Cable difference is typing after room deletion: Go ignores commands for the deleted room, while Rust echoes them. Both render the deleted-room composer message. See the [Cable trace](validation/deleted-room-cable.diff).
- Exotic malformed parameter/content-negotiation combinations are not exhaustively equivalent. The tested routes, valid browser workflows, signing/storage contracts, and upgrade checks are the support for compatibility; they are not a proof for every possible HTTP input.

Known implementation choices are also recorded in [README.md](../README.md#known-differences). The benchmark uses explicit functional contracts rather than treating this stricter all-layer comparison as a pass.

## Optimization verification — 2026-10-03

The optimized binary passes [formatting, vet and race tests](validation/optimization/check.txt),
including the local WebSocket extension and upstream WebSocket tests. Added regression checks
cover sidebar cache invalidation, recorded message bytes/conditional responses, publication
session/membership revocation, and the focused plain-text path throughout the rich-text corpus.
[Browser workflows](validation/optimization/browser.txt), [bidirectional cookie/message upgrade](validation/optimization/upgrade.txt),
and [production container setup, backup, restore and restart](validation/optimization/container.txt)
pass with the optimized build.

A fresh complete default-seed desktop run covered 192 cells: 178/178 applicable screenshots
and accessibility comparisons matched, with zero capture errors. Server DOM matched 19/192,
live DOM 8/178, network 0/178, and Cable 177/178. The same deleted-room typing difference
remains. Eight phone light/dark cells all matched pixels, accessibility and Cable; their
DOM/network differences remain. Strict whole-cell parity therefore still fails.

Reports and binary hashes: [desktop](validation/optimization/default.json),
[desktop metadata](validation/optimization/default.metadata.json),
[phone](validation/optimization/phone.json), [phone metadata](validation/optimization/phone.metadata.json).
No new masks or allowlists were added. The earlier alternative-seed runs above were not repeated
for this optimization. Full local artifacts are in `.cache/screens-optimized*`.

The [optimized application benchmark](../bench/results/application-optimized-20261003/report.md)
completed all six application runs: 126 HTTP samples, 36 Cable configurations, 506,897
acknowledged HTTP writes checked in both messages and FTS, and 30 identical-byte thumbnails.
There were zero HTTP errors or incomplete deliveries. Interrupted repetitions affected by
external builds were excluded and rerun; the report records those interruptions and resumption.

## Further optimizations — 2026-10-03

The [next optimization record](../bench/results/optimization-next-20261003/README.md) covers
room-page HTML caching, focused rich-text processing and writer statement caching. Formatting,
vet and race tests pass, along with browser workflows and upgrade interoperability. All 75
applicable screenshots/accessibility trees in 77 targeted captures match; strict DOM/network
and the existing deleted-room typing difference remain. An initial navigation AbortError during
concurrent browser runs is retained in the record; a standalone rerun passed without masking it.
The focused three-binary benchmark verified 345,913 writes and nine identical-byte thumbnails
with zero HTTP errors. It did not repeat the full Cable workload or production-container checks.

## Engine fork — M0 environment (2026-10-05)

- Fork: `engine` branch of basecamp/once-campfire-go at `8d2f7f2`, plus design
  (`plans/2026-10-05-engine-design.md`) and plan (`plans/2026-10-05-engine.md`).
- Host: AMD Ryzen AI MAX+ 395, 32 threads, Omarchy; Go 1.27.1 via mise. The
  official Elixir harness rebuilds its loadgen with `mise exec rust@1.98.1`;
  that toolchain is installed for this machine. The Rust checkout itself built
  cleanly with the on-PATH cargo 1.97.1.
- Go build: `bin/build` produced `campfire` (39,689,520 B); `/up` answered 200
  on both listeners; `go test -tags sqlite_fts5 ./...` PASS on the unmodified
  tree. Published-baseline binary preserved at
  `tmp/baseline/campfire-published` sha256
  `6b69111097e197ce737df7efe1026efe74aa2400c1561b268d7b3d4d68b163ab`.
- Rust checkout: once-campfire-rust at `ccece30` with reference submodule
  `90b3300`; release binary sha256
  `c027ef78cf9f55ff64fe94191853930d5c427a5facd3d2395267296d19dcc2ad`; shared
  loadgen sha256
  `e88f7d0c0b0bc5a53bc5d4d43f0ea1a23def2c0be5a685420f74d0d697af5379`; default
  seed DB sha256
  `64ecbb80af6ad55fb1f73a9b0cc261f0f33744bf1e447970405c15c145a5e88c`; Rails
  reference image `campfire-reference:app` (`ed09003899ed`).
- Elixir harness checkout: once-campfire-elixir at `f15fc9e`; its loadgen
  built. Gate note: `bench/run` runs `bench/validate.py ledger`, which demands a
  complete `bin/verify-parity` run of the Elixir application whose committed
  verification digest is stale on a clean clone (upstream history was
  densified). Baseline reproduction therefore uses the Rust repository's
  container harness (Rails vs Rust, same seed, same CPU pinning) and the Go
  repository's native A/B harness (Go vs Rust binaries, same loadgen). The
  Elixir harness is reserved for the final cross-language comparison; its gate
  will be satisfied by a full `verify-parity` run or bypassed by a local,
  explicitly documented change that touches only the Elixir-provenance
  assertion, never the load generator, preflight or report code.

## Engine fork — M0 baseline reproduction (2026-10-05/06)

Official-style container comparison (Rust repository harness, production images,
same seed `64ecbb80…`, servers on CPUs 8-11, loadgen on 12-15, host network,
HTTP suite, 2 rotating reps, `HTTP_SECS=4`). Medians [min–max], c=16 req/s:

| Route | Rails | Rust (this machine) | Published Rust |
|---|---:|---:|---:|
| room_show | 209 [201–217] | 37,015 [36,796–37,235] | 36,260 |
| messages_page | 400 [388–412] | 42,051 [41,553–42,550] | 40,872 |
| sidebar | 517 [514–521] | 34,911 [34,582–35,240] | 34,672 |
| search | 385 [380–391] | 34,450 [34,196–34,704] | 33,299 |
| post_message | 259 [252–266] | 6,743 [6,736–6,750] | 6,896 |

Rails and Rust reproduce the published table within a few percent on this
machine. Raw: `../once-campfire-rust/bench/results/baseline-container-20261005/`.

Native Go-vs-Rust baseline (Go repository `bench/application`, identities,
3 alternating reps, 5 s samples, c=16):

| Route | Go | Rust | Rust/Go |
|---|---:|---:|---:|
| room_show | 16,895 (15,729–17,113) | 30,455 (18,419–30,825) | 1.80× |
| messages_page | 23,828 (21,658–23,842) | 34,637 (13,789–35,086) | 1.45× |
| sidebar | 25,804 (24,407–28,912) | 39,420 (24,640–40,270) | 1.53× |
| search | 16,001 (15,905–16,033) | 31,631 (29,378–32,537) | 1.98× |
| post_message | 4,753 (2,044–5,100) | 6,760 (6,595–7,761) | 1.42× |

Zero HTTP errors; 227,621 acknowledged writes verified in both messages and FTS;
six identical-byte thumbnails. Response-size context: Go room_show body is
374,036 B uncompressed — the target of the engine's compressed-piece design.
Raw: `bench/results/baseline-native-20261005/` (report, raw JSON, metadata with
load averages). Note: light development activity (nice'd, on non-pinned CPUs)
overlapped part of the native run; every sample records its load average.

## Engine — first post-engine measurement (ENGINE-17, 2026-10-06)

Native `bench/application`, 3 rotating reps, 5 s samples, c=16, identity,
CPUs 8-11/12-15, load ~2.5 recorded in metadata. Medians [min–max] req/s:

| Route | Published Go | Engine Go | Rust | Engine gain | Gap to Rust |
|---|---:|---:|---:|---:|---:|
| room_show | 17,120 [17,085–17,229] | 24,519 [24,474–24,556] | 31,162 [31,030–31,212] | +43% | 1.27× |
| search | 16,178 [16,176–16,181] | 26,865 [26,770–26,895] | 33,010 [32,646–33,023] | +66% | 1.23× |
| messages_page | 24,010 [24,006–24,076] | 25,215 [25,203–25,363] | 35,669 [35,409–35,738] | +5% | 1.41× |
| sidebar | 27,266 [26,524–29,199] | 26,664 [25,271–27,645] | 40,752 [40,651–40,902] | −2% | 1.53× |
| post_message | 5,376 [5,252–5,420] | 5,399 [5,272–5,442] | 7,952 [7,824–8,059] | 0% | 1.47× |

Room p99 3.59→2.59 ms; search p99 3.28→1.89 ms. Raw:
`bench/results/engine-20261006/` (report, raw JSON, metadata).

Interpretation: ENGINE-16's recorded-response pieces moved the flagship rows
(room +43%, search +66%), halving the Rust gap on both. Sidebar and
post_message are outside the recorded-response path by design (fragment path
and write path respectively) and are unmoved; messages_page gains little.
Those three plus the remaining room/search gap are the next targets.

## Official harness checkpoint (2026-10-06, engine image pre-ENGINE-18)

Official Elixir harness, production Docker images, HTTP suite, 2 rotating reps,
c=16, seed `64ecbb80`, CPUs 8-11/12-15. Rust reproduces the published table
(36,378 vs 36,260 room), validating the run.

| Route c=16 | Rails | Go (engine) | Rust | Gap to Rust |
|---|---:|---:|---:|---:|
| room_show | 224 | 34,546 | 36,378 | 1.05× |
| search | 380 | 26,368 | 34,620 | 1.31× |
| messages_page | 395 | 30,705 | 41,860 | 1.36× |
| post_message | 265 | 4,828 | 6,746 | 1.40× |
| sidebar | 528 | 19,987 | 35,085 | 1.76× |

CPU µs/success: room 110 vs 105; search 139 vs 96; messages 123 vs 90;
post 552 vs 379; sidebar 186 vs 109. Zero HTTP errors. Raw:
`../once-campfire-elixir/bench/results/engine-official-ckpt/`.

## ENGINE-18 measurement (2026-10-06, fastdb read wiring)

Native A/B (engine17 binary = go-before vs engine18 = go vs Rust), 3 rotating
reps, c=16, medians. Note: taken while five parallel implementation streams were
active; Rust medians are tight and the rotating order absorbs shared load.

| Route | engine17 | engine18 | gain | Rust | gap |
|---|---:|---:|---:|---:|---:|
| room_show | 24,262 | 27,985 | +15.3% | 30,842 | 1.10× |
| messages_page | 25,065 | 28,633 | +14.2% | 35,527 | 1.24× |
| sidebar | 26,072 | 34,161 | +31.0% | 40,557 | 1.19× |
| search | 26,766 | 27,496 | +2.7% | 32,711 | 1.19× |
| post_message | 5,271 | 5,271 | 0% | 7,909 | 1.50× |

Sidebar and room confirm the profile's database/sql-glue share; search/post were
not wired (ENGINE-30/31 own them). Raw: `bench/results/engine18-20261006/`.

## ENGINE-30 — fastdb FTS reads and versioned search result cache (2026-10-06)

`internal/fastdb/search.go` mirrors `database.DB.Search` (same MATCH word fold, quoted tokens, membership join, LIMIT 100 reversal); `internal/web/search_cache.go` adds a byte-bounded LRU of search pages keyed by (user, normalized query, corpusVersion, membershipVersion) with `CAMPFIRE_SEARCH_CACHE` (default on; off rolls back to the database/sql reads) and `CAMPFIRE_SEARCH_CACHE_MB` (default 16). The corpus version is a global counter bumped after every committed message create/edit/delete, boost change and room destroy; the membership version after every membership grant/revocation, involvement change and account create/deactivate. POST/DELETE recent-search routes purge the user's entries.

Check: `timeout 900 nice -n 19 env GOMAXPROCS=4 go test -race -count=1 -timeout 800s -p 2 -tags sqlite_fts5 ./...` — pass. `go vet -tags sqlite_fts5 ./...` — clean. `gofmt -l .` — empty.

Coverage: differential fastdb-vs-database/sql search on the seed and synthetic fixture (empty results, membership filtering, quoted tokens, unicode fold) and a byte-equality test of the word fold; on/off response byte parity incl. gzip, ETag and 304; corpus poisoning (edit/delete after fill) plus a DROP TABLE message_search_index proof that a hit skips the FTS scan; membership grant/revoke invalidation; POST/DELETE recent-search purge semantics; a 60-step deterministic write+query fuzz with per-step on/off byte parity.

Package bench (`nice -n 19 env GOMAXPROCS=4 go test -count=1 -timeout 250s -p 2 -tags sqlite_fts5 -bench '^BenchmarkSearch$' -benchmem -run '^$' ./internal/fastdb/`): 9.06 µs/op, 79 B/op, 5 allocs/op (parity-seed fixture; per-request query normalization and MATCH text are the allocations). Official harness rerun (search row after ENGINE-30) is ENGINE-32's measurement step.
## ENGINE-40 record (2026-10-06, cable fan-out fast path)

Shipped behind `CAMPFIRE_CABLE_FAST` (default on; `off` = legacy one-write-per-frame
path). Three pieces:

- Read: server connections no longer retain net/http's hijacked 4KiB read buffer; a
  freshly accepted socket holds nothing, the first frame lazily allocates a 512-byte
  scratch (`readScratchSize`), and payload reads at or above that size bypass the
  scratch with one exact-length syscall. `Conn.Read` now allocates exactly
  `payloadLength` and reads it directly; fragmented/compressed messages hand off to
  the unchanged streaming reader. Wire bytes identical (byte-for-byte
  batch-vs-sequential test + full upstream suite).
- Write: `WritePreparedBatch` coalesces up to 64 frames into one vectored write
  (writev on TCP conns), reuse scratch, no per-batch allocation. Hub drains the
  256-frame queue per wake into one batch; slow-client overflow policy unchanged.
- Frame cache: per-(scope, identifier) exact-payload reuse with bounded FIFO
  retention (8 payloads per key, 4096 entries / 8 MiB global), on top of prepared
  message sharing; equality requires full payload bytes (no poisonable state).

Micro-benchmarks (net.Pipe, GOMAXPROCS=4, both directions included):

| Benchmark | Single | Batch(8) | Δ |
|---|---:|---:|---:|
| WritePrepared per message | 6,946 ns | 1,199 ns | ×5.8 |
| server Read (exact vs Reader+ReadAll) | 1,308 ns | 1,065 ns | −19% |

Alloc: exact read 128 B/op vs legacy 512 B/op (exact-size payload). Application-level
fan-out A/B (bench/application --cable-clients 100/1000/10000 vs Rust) is still owed:
the shared harness run is outside this task. Deliberate difference from legacy: the
30 s write deadline now bounds the whole batch (one wake) rather than each frame.

Differences vs legacy path verified byte-for-byte on a broadcast corpus (small,
unicode, empty, compressed, repeated payloads) over a real socket: identical decoded
payloads and identifiers with the fast path on and off.

## Merged engine benchmark (2026-10-07, all ten branches)

Native `bench/application`, 3 rotating reps, c=16, medians. `go-before` =
pre-merge engine18 binary; `go` = merged `bc31205` (577beb7a). Validation:
9 runs, 401,414 writes verified in messages+FTS, 0 HTTP errors, 0 incomplete
deliveries.

| Route | engine18 | Merged | Rust | Merged vs Rust |
|---|---:|---:|---:|---:|
| room_show | 27,957 | 41,216 | 30,959 | **1.33×** |
| messages_page | 28,340 | 49,632 | 35,529 | **1.40×** |
| sidebar | 31,607 | 126,859 | 40,478 | **3.13×** |
| search | 27,695 | 72,759 | 32,832 | **2.22×** |
| post_message | 5,263 | 6,570 | 7,873 | 0.83× |
| Cable 100 d=1 | 2,428 | 3,088 | 3,347 | 0.92× |
| Cable 100 d=0 | 2,265 | 3,589 | 4,273 | 0.84× |
| Cable 1000 d=1 | ~305 | 329 | ~365 | ~0.90× |
| Cable 1000 d=0 | ~307 | ~515 | ~548 | ~0.94× |

Room p50 0.261 ms vs Rust 0.499; sidebar p50 0.097 vs 0.383. p99 tails for
room/messages/post remain worse than Rust (2.55/2.31/13.0 ms vs 0.93/0.76/5.5).
Raw: `bench/results/merged-20261007/`.

## Official merged acceptance run (2026-10-07, once-campfire-go:merged)

Elixir harness, production Docker images, HTTP + cable, 2 rotating reps, c=16
medians [min–max], same seed for all apps (note: seed hash at run start was
`fea8d18a…`, mutated from the canonical `64ecbb80…`; applied identically to
every app and responses validated, so internally fair — restore before the
final run). Zero HTTP errors for all apps; responses gzip-validated with body
hashes in `*-validation.json`.

| Route c=16 | Rails | Go merged | Rust | Go vs Rust |
|---|---:|---:|---:|---:|
| room_show | 201 | 63,890 | 36,613 | **1.75×** |
| messages_page | 407 | 68,910 | 41,986 | **1.64×** |
| sidebar | 536 | 55,831 | 34,940 | **1.60×** |
| search | 369 | 74,264 | 34,267 | **2.17×** |
| post_message | 252 | 5,468 | 6,756 | 0.81× |
| up | 4,182 | 178,323 | 245,202 | 0.73× |
| avatar | 94,920 | 216,792 | 394,856 | 0.55× |
| static_css | 132,466 | 311,152 | 414,252 | 0.75× |

CPU µs/success: room 58.0 vs 104; messages 53.6 vs 90.1; sidebar 69.0 vs 109;
search 49.8 vs 97.1; post 427 vs 381. Go does roughly half Rust's CPU on every
read row. Against the published Go row (room 3,860) this is 16.6×.

Cable: connect+subscribe tied (0.06 s / 0.15 s at 100/1000). 100 clients:
paced all-clients p50 2.16 vs 2.05 ms, sustained 3,354 vs 4,061 msgs/s (0.83×).
1000 clients: paced p50 6.90 vs 6.04 ms, p99 18.1 vs 8.38, sustained 514 vs 545
(0.94×), saturated post→all p50 13.7 vs 15.8 ms (**Go faster**). Memory:
151→225 MiB Go vs 119→126 MiB Rust (5–10× below Rails).

Raw: `../once-campfire-elixir/bench/results/merged-official-20261007/`.
## ENGINE-40b record (2026-10-07, versioned authorization cache and lock-free fan-out)

The remaining cable gap vs Rust (def late ~14%, theirs ~22%) was the publish/delivery
path: per-publish SQL authorization and per-recipient allocation. Shipped behind the same
`CAMPFIRE_CABLE_FAST` gate (`off` = legacy per-publish `AuthorizedSessions` query, byte-
identical fan-out). Three pieces:

- Versioned authorization cache: `(room, token) -> authorized user` on the hub, keyed
  additionally by three in-process generations — `SessionVersion` (sessions-table
  inserts/deletes: login, logout, ban, deactivation), `MembershipVersion` (existing
  ENGINE-30 counter; membership grant/revocation and room-visibility writes) and
  `UserVersion` (users.status writes: ban/unban/deactivate). An entry stores the triple it
  was queried under and a lookup serves it only while the triple still matches, so no
  authorization result outlives its generations; a store on the result of a newer query is
  impossible by construction. Cache bound 2^16 entries, FIFO ring eviction. Steady-state
  publishes run zero SQL; only misses (after a bump or on a fresh token) run one batched
  `AuthorizedSessions` query per room. Query errors cancel but store nothing, so a
  transient read failure cannot poison the cache. Cache entries survive revocation only
  via generation bumps — the poisoning contract ("a revoked/banned/removed client is never
  served from cache") is pinned in tests. Bumps are all post-commit, in the audited write
  helpers; `DeleteSession` was added (and web logout routed through it) so the logout
  write bumps too. Out-of-band SQL is not visible until a bump or restart (documented
  single-process limit, same as the sidebar/search caches).
- Forward fan-out: recipients are snapshotted under the hub read lock into a slice
  pre-sized to the client count (one allocation, no growth), then authorized and delivered
  with no hub lock held. All authorization lookups for one publish happen under a single
  cache lock acquisition; per-recipient cost is one map lookup plus one channel send.
- Subscribe/connect stays bounded at 1000 clients: the subscribe path performs O(1)
  per-connection work on the hub lock (map ops) plus one or two DB reads; no O(n) shared
  work was introduced or found.

Micro-benchmark (BenchmarkPublishFanout, N clients each with a distinct session, drained
queues, warm cache, `taskset -c 16-31 nice -n 19 env GOMAXPROCS=4`, min of 5):

| N | path | ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| 100 | before | 110,001 | 49,452 | 446 |
| 100 | after | 11,085 | 6,896 | 7 |
| 1000 | before | 1,156,725 | 519,061 | 4,063 |
| 1000 | after | 110,655 | 58,116 | 7 |

9.9× / 10.5× faster; the before-allocs are dominated by the per-publish
`AuthorizedSessions` batch (JSON encode of N tokens plus a per-row scan); the cache removes
the whole query from the steady state, leaving the recipients snapshot (one pre-sized
slice), the frames map and the frame-cache bookkeeping.

Full suite (mandated command): pass with -race, -p 2, sqlite_fts5; nested
`third_party/websocket` module suite: pass; `go vet -tags sqlite_fts5 ./...`: clean;
`gofmt -l .`: empty. Coverage: poisoning tests for ban/logout/membership-removal (fill,
revoke, assert next publication excludes, stayer unaffected, unban+relogin restores),
generation-lifetime test, write-helper audit enumeration in
`internal/database/versions_test.go` (+ quiet cases for refresh-session, user rename/role,
message writes, presence, push subscriptions), concurrent publish (every recipient gets
every payload) and concurrent publish+revocation storm under -race, existing frame byte
parity fast-vs-legacy and 100/1000 simulated delivery tests unchanged.

## Impeccable-correctness matrix (2026-10-07, pre-final)

Nothing is claimed or PRed until every applicable box is green with evidence.

| # | Check | Status |
|---|---|---|
| 1 | Single-member gzip fix (`engine-fix-gzip`, browser-safe splicing) | running |
| 2 | Playwright proof: room/messages/search render full message lists in Chromium | pending 1 |
| 3 | Full `-race -tags sqlite_fts5 ./...` on the merged tree | pending merge |
| 4 | `bin/check` (gofmt/assets/vet/race) + `bin/check-assets` | pending merge |
| 5 | `bin/check-upgrade --rust-root ../once-campfire-rust` (Go↔Rust interop) | pending merge |
| 6 | `bin/check-browser.mjs` full workflow flow | pending 1 |
| 7 | `bin/check-screens` pixels+aria equality (DOM/network deltas are pre-existing, documented) | pending 1 |
| 8 | Native harness write verification (messages + FTS) and delivery completeness | pending merge |
| 9 | Official acceptance: Rails + Rust (both revisions, built here) + Go, 3 reps, HTTP c=1/16/64, cable 100/500/1000; loadgen/server CPU recorded | pending 8 |
| 10 | Merged-version profile | pending 9 |
| 11 | `bin/check-container` + backup/restore | release item |
| 12 | Fresh canonical seed shared by all apps; raw artifacts retained | with 9 |

Known, documented limits carried into the record: strict DOM/network parity
differences vs Rails (pre-existing upstream); single-process cache coherence;
write-lane crash windows; frozen-time test artifacts. The engine's behaviour
preservation is proven separately by on/off byte-parity, poisoning and
invalidation-audit tests.

## Final native verification run (2026-10-07, merged + browser-safe gzip fix)

3 rotating reps, c=16, medians [min–max]; `go-before` = engine18 baseline.
Validation: 9 runs, 394,573 acknowledged writes verified in messages+FTS,
0 HTTP errors, 0 incomplete deliveries, 9 identical thumbnails.

| Route c=16 | engine18 | Final | Rust | Final vs Rust |
|---|---:|---:|---:|---:|
| room_show | 27,936 | 47,916 | 30,813 | **1.56×** |
| messages_page | 28,640 | 54,976 | 35,424 | **1.55×** |
| sidebar | 32,044 | 129,331 | 40,227 | **3.21×** |
| search | 27,704 | 92,451 | 32,702 | **2.83×** |
| post_message | 5,266 | 6,282 | 7,842 | 0.80× |
| Cable 1000 d=1 | ~305 | ~326 | ~364 | 0.90× |

Room p99 1.99 ms vs Rust 0.94; sidebar p99 0.56 vs 0.71 (faster). Raw:
`bench/results/final-native-20261007/`.

## FINAL official run (2026-10-07, fresh seed, gzip-fix tree, 3 reps)

Elixir harness, production images, fresh seed `9f6241dc`, 3 rotating reps,
c=16 medians, zero HTTP errors for all apps. Go image `once-campfire-go:final`
(1f9fb2be, merged browser-safe gzip fix). Pre-ENGINE-45b/52/53/54.

| Route c=16 | Rails | Go final | Rust | Go vs Rust | Go CPU µs | Rust CPU µs |
|---|---:|---:|---:|---:|---:|---:|
| room_show | 218 | 77,456 | 36,805 | **2.10×** | 48.2 | 103 |
| messages_page | 408 | 81,412 | 41,923 | **1.94×** | 45.0 | 90.1 |
| sidebar | 535 | 55,904 | 35,295 | **1.58×** | 69.0 | 108 |
| search | 387 | 101,513 | 34,442 | **2.95×** | 36.3 | 97.6 |
| post_message | 274 | 5,290 | 6,747 | 0.78× | 422 | 380 |
| up | 4,199 | 181,792 | 242,700 | 0.75× | — | — |
| avatar | 98,072 | 214,121 | 401,286 | 0.53× | — | — |
| static_css | 135,488 | 307,544 | 423,514 | 0.73× | — | — |

Cable sustained (delivered to all): 100 clients Go 3,523 vs Rust 4,010 (0.88×);
500 clients 1,016 vs 1,122 (0.91×); 1000 clients 483 vs 540 (0.89×).
Saturated post→all p50: Go faster at 500 (6.63 vs 7.97 ms) and 1000
(14.1 vs 16.0 ms). Connect+subscribe tied/slightly behind (0.15 vs 0.13 s at
1000). Memory: Go 158–225 MiB vs Rust 122–124 (both far below Rails 872–1399).

Raw: `../once-campfire-elixir/bench/results/FINAL-official-20261007/`.
This is the result of record for the gzip-fix tree; ENGINE-45b/52/53/54 are in
flight and will be followed by one more official run for the complete set.
## ENGINE-52 record (2026-10-07, auxiliary-route front fast paths)

The three auxiliary rows of the official acceptance run — up, avatar,
static_css — are tiny fixed responses whose gap to Rust was per-response
overhead in the public front path (`merged-official-20261007`: up 0.73×,
avatar 0.55×, static_css 0.75×). This record closes the per-request work:

- **Fixed `/up` table** (`CAMPFIRE_FRONT_FIXED`, default `on`; `off` = the
  application path, byte for byte): the first unconditional GET captures the
  health response once per content encoding (identity and gzip — the chain
  runs a second pass with the other Accept-Encoding and the captured gzip
  body is verified to decode to the identity bytes), and every later request
  replays the captured bytes without touching sessions, the arena, the
  response buffer or the application. The table is keyed by host, path and
  the exact Accept value (the application negotiates the health format from
  Accept and emits `Vary: Accept`), bounded at 16 pairs, and conditional,
  Range, Upgrade, non-negotiable-encoding and uncaptured-Accept requests
  still reach the application. Replays carry the exact captured headers —
  validators, `Vary`, `Content-Encoding`, `X-Cache: miss` — so the wire
  bytes are identical to the application path, chunked framing included
  (the application path emits no Content-Length, so neither does a replay).
- **Replay lane of the ordinary response cache**: the compression wrapper's
  buffering/decision pass is skipped for recorded replays (header policy
  still runs in `WriteHeader`), and the per-header copied header slices
  share one allocation. Wire-identical by construction.
- **Encoding negotiation memo**: the public-layer per-request
  Accept-Encoding parse is memoized per header value (bounded; the decision
  is a pure function of the value). The count includes the identity path:
  `identity` must not be starved out by an unbounded table, so the cap is 128
  with a wholesale clear.
- **`forward` no longer clones the request**: net/http allocates a fresh
  `Request` and header map per request (`readRequestLimit`), so the clone —
  the largest per-request allocation in the public chain for the
  cookie-bearing loadgen requests — was pure overhead. The header edits are
  made in place; `X-Forwarded-*` behavior is unchanged.

Parity: `internal/front/parity_test.go` and `internal/web/aux_parity_test.go`
compare the fast paths against the application path byte for byte (status,
headers minus Date, raw body) across gzip/identity/absent/zstd-only
Accept-Encoding, no-Accept/json/browser Accept, cookie-bearing requests,
HEAD, rejected encodings and If-None-Match conditionals, and pin that the
fixed table serves without invoking the application after the first fill.

Package micro-benchmarks (`taskset -c 16-31 nice -n 19 env GOMAXPROCS=4`,
single keep-alive connection, loadgen header shape; server chain only,
handler over a shim writer, all three routes):

| Benchmark | Before | After | Alloc before | Alloc after |
|---|---:|---:|---:|---:|
| FrontDirectUp (fixed replay) | 4,942 ns, 4,963 B, 42 allocs | 1,485 ns, 1,784 B, 18 allocs | ×3.3 | −57% |
| FrontDirectAvatarHit | 2,034 ns, 3,048 B, 35 allocs | 1,603 ns, 2,072 B, 20 allocs | ×1.27 | −43% |
| FrontDirectStaticCSSHit | 1,971 ns, 2,840 B, 35 allocs | 1,411 ns, 1,848 B, 20 allocs | ×1.40 | −43% |

Full-chain end-to-end (same harness, server+client share the process):
FrontUp 22.6 → 17.1 µs, FrontAvatarHit 22.4 → 20.9 µs, FrontStaticCSSHit
18.7 → 18.0 µs, FrontUpLogged 22.4 → 18.1 µs (the log line still costs
~1.8 µs; `LOG_REQUESTS` stays on by default). The residual hit-path cost
is net/http's per-request parse/write machinery and socket syscalls; the
measured trivial-handler floor for this harness is ~14 µs, so the front
chain itself now sits ~1.5 µs above the floor. Adding Content-Length to
replays (which would remove chunked framing) was deliberately not done:
the application path emits no Content-Length, and the mandate is byte
identity. A precomposed public-listener writer (fastserve) is the next
step beyond this record.

## Consolidation pass on the full 7-branch tree (2026-10-07)

- `bin/check` (gofmt/assets/vet/full `-race` suite): EXIT=0
- Browser workflows (full setup → live two-tab → edit → search → admin → bots →
  transfers → pings): EXIT=0, PASS
- Screen inventory `--only '**'`: 192 cells, 0 errors; **178/178 applicable
  pixels and accessibility equal** (14 non-visual cells: fragments/JSON/PWA/
  avatars); remaining strict DOM/network deltas are the documented pre-existing
  differences.
- Upgrade interop (Rust↔Go logins, Go-written message searched by Rust): EXIT=0
- Regression found and fixed during this pass: precomposed framing omitted the
  head-terminating blank line on keep-alive responses (search page reached real
  browsers as a malformed header block; proxies returned 502). Fixed in
  `35661c9` with a regression test pinning the keep-alive path.

## FINAL3 official acceptance (2026-10-07, production images, 3 reps)

Elixir harness, Rails/Rust/Go production images, fresh seed `9f6241dc`,
3 rotating reps, c=16 medians [min–max], zero HTTP errors. Go image
`once-campfire-go:final3` (`bcd375a47bdc`, engine `b9bca18` with the
upgrade-handoff cable fix). Rust and Rails built and measured on this machine.

| Route c=16 | Rails | Go final3 | Rust | Go vs Rust | CPU/req Go vs Rust |
|---|---:|---:|---:|---:|---:|
| room_show | 220 | **79,343** | 36,859 | **2.15×** | 47 vs 103 µs |
| search | 389 | **103,753** | 34,734 | **2.99×** | 35.5 vs 95.8 |
| messages_page | 415 | **81,918** | 41,909 | **1.95×** | 44.6 vs 89.9 |
| sidebar | 529 | **56,204** | 35,580 | **1.58×** | 68.8 vs 107 |
| post_message | 273 | **8,473** | 6,667 | **1.27×** | 308 vs 381 |
| up | 4,207 | **274,711** | 245,753 | **1.12×** | — |
| avatar | 97,534 | 221,149 | 382,877 | 0.58× | — |
| static_css | 137,157 | 329,394 | 442,263 | 0.74× | — |

Cable (delivered to all): 100 clients **5,049 vs Rust 4,033 (1.25×)**;
500 clients 1,094 vs 1,112 (parity); 1000 clients 520 vs 552 (0.94×).
Saturated post→all p50: Go faster at every size (0.87/6.41/13.6 ms vs
1.03/7.96/15.7). Connect+subscribe at parity. Memory at 1000 clients:
Go 200–233 MiB vs Rust ~101–123 (Rails 1,124–1,406).

Correctness on this tree: `bin/check` green, browser workflows PASS,
178/178 applicable screen pixels+a11y, Go↔Rust interop PASS,
native run 463,937 writes verified in messages+FTS, 0 delivery failures.
Raw: `../once-campfire-elixir/bench/results/FINAL3-official-20261007/`.

## FINAL4 official acceptance (2026-10-07, public loop + cable wake path)

Elixir harness, production images, 3 rotating reps, c=16 medians, zero HTTP
errors. Go image `once-campfire-go:final4` (engine `cfdc2e4`; ENGINE-61 cable
wake path + ENGINE-62 public-loop/replay lane). Rust and Rails on this machine.

| Route c=16 | Rails | Go final4 | Rust | Go vs Rust | CPU/req |
|---|---:|---:|---:|---:|---:|
| room_show | 195 | 86,262 | 36,370 | **2.37×** | 43.2 vs 105 µs |
| messages_page | 414 | 93,404 | 41,553 | **2.25×** | 39.5 vs 90.9 |
| sidebar | 517 | 55,014 | 35,028 | **1.57×** | 68.1 vs 109 µs |
| search | 371 | 117,661 | 34,575 | **3.40×** | 31.5 vs 96.6 µs |
| post_message | 253 | 8,391 | 6,766 | **1.24×** | 299 vs 381 µs |
| up | 4,151 | 424,345 | 245,305 | **1.73×** | 7.2 vs 15 µs |
| static_css | 134,939 | 422,232 | 383,734 | **1.10×** | 7.35 vs 7.82 µs |
| avatar | 97,894 | 377,043 | 382,537 | 0.99× (parity) | 8.35 vs 7.96 µs |

Cable sustained: 100 clients **5,343 vs 4,085 (1.31×)**; 500 clients
1,121 vs 1,123 (parity 0.998×); 1,000 clients **548 vs 544 (1.007×)**.

Every published row ahead (1.24×–3.40×); cable ahead at 100 and 1000, parity
at 500; assets at parity or ahead except avatar at 0.99× (one noisy rep in
the min range; medians within spread). Correctness on `cfdc2e4`: bin/check
EXIT 0, browser PASS, 178/178 screen pixels+a11y, interop PASS, native run
463k writes verified, zero delivery failures. Raw:
`../once-campfire-elixir/bench/results/FINAL4-official-20261007/`.
## ENGINE-62 record (2026-10-07, owned loop + precomposed assets on the public listener)

The `up` row crossed in FINAL3 (274,711 vs 245,753); avatar stood at 0.58×
and static_css at 0.74× because the remaining per-request cost was
`net/http` on the **public** listener (~18 vs ~8-10 µs CPU per success in the
official FINAL3 accounting), while the owned loop (fastserve, ~6.3 µs per
exchange) only served the internal listener. ENGINE-62 closes the front
transport and write path:

- **Public listener on the owned loop when TLS is not configured**
  (DISABLE_SSL / plain HTTP — the benchmark configuration). TLS/ACME
  listeners keep net/http + autocert unchanged. `CAMPFIRE_SERVER_LOOP=off`
  rolls both listeners back. The loop's documented strictness deltas now
  apply to the public listener as well; `TestServerLoopFlagDiff` runs the
  full public composition both ways and stays byte-equal.
- **Recorded replay lane**: every cache hit (ordinary or fixed `/up`) emits
  status line + a precomputed header block (rendered once per entry from the
  post-policy capture with the X-Cache marker substituted, sorted and
  sanitized exactly like `composeHead`) + body through
  `fastserve.response.WriteRecorded`: no header map, no sort, no middleware,
  one writev, with the receiver making the map path's auto Content-Length /
  chunked / close and 304 decisions. The lane engages only for HTTP/1.1
  GET/HEAD without request bodies against entries with reproducible heads
  (no Date/Content-Length/Transfer-Encoding/Trailer/Connection captured,
  Content-Type present when a body exists, and the capture's
  Content-Encoding unchanged by the public policy); everything else takes
  the byte-identical map path. Wire parity pinned by
  `internal/front/recorded_test.go` against the same-loop map path and the
  net/http public path across gzip/identity/zstd-only/gzip+zstd/absent, HEAD,
  304, Range and keep-alive streams.
- `recordResponse` no longer implements Unwrap: on the public listener the
  web layer's precomposed-receiver walk stops at the cache, so X-Cache and
  the capture always participate (the internal listener is unaffected).
- Request path: a `canonicalHeaderKey` fast path for the common header names
  (textproto fallback for the rest) and the dead idle-deadline clear dropped
  (one fewer syscall per keep-alive request).
- **Async access-log pipeline** (cmd/campfire/accesslog.go): the per-request
  access log (LOG_REQUESTS, default on) is the hottest write in the public
  chain — every request used to queue on the shared slog formatting lock and
  do one small write to the log pipe, so a slow sink stalled whole
  measurement windows. Records are now appended to a bounded queue (no
  formatting, no allocation, strings alias the request) and a dedicated
  goroutine renders them with slog's exact spelling (a hand-rolled fast path
  pinned byte-for-byte by `TestAccessLogFastPathPins`; the slog fallback
  covers values needing quoting) and drains to stderr in 2 ms / 64 KiB
  batches. Line content, ordering, the default and the destination are
  unchanged; overflow drops records with a warning instead of blocking.

Package micro-benchmarks (`taskset -c 16-31 nice -n 19 env GOMAXPROCS=4`,
single keep-alive connection, loadgen header shape; server+client in
process; before = FINAL3 tree, net/http public chain):

| Benchmark | Before (net/http) | After (loop+recorded) |
|---|---:|---:|
| FrontUp | 17.3 µs | 15.2 µs (LoopUp; 97 allocs) |
| FrontAvatarHit | 20.8 µs | 18.8 µs (LoopAvatarHit; 111 allocs) |
| FrontStaticCSSHit | 18.2 µs | 15.8 µs (LoopStaticCSSHit; 101 allocs) |
| LoopAvatarHit map path (lane hidden) | — | 22.3 µs |
| LoopStaticCSSHit map path (lane hidden) | — | 17.1 µs |

The map-path rows show the loop and lane contributions separately: the loop
is worth ~1.7-2 µs end to end and the recorded lane another ~3 µs on the
chunked avatar.

Same-conditions A/B against the pinned Rust loadgen (native binaries, seed
9f6241dc, server and loadgen each pinned to 4 CPUs, c=16, gzip, keep-alive,
rotating reps; `tmp/local_ab.py`), clean windows (medians):

| Route c=16 | go-before | go-after | Rust | go-after vs Rust |
|---|---:|---:|---:|---:|
| avatar rps | ~234k | ~400k | ~484k | 0.83× |
| avatar CPU µs/success | 14.7 | 6.8 | 7.3 | Go lower |
| static_css rps | ~361k | ~480k | ~500k | 0.96× |
| static_css CPU µs/success | 9.4 | 5.9 | 6.9 | Go lower |
| up rps | ~296k | ~470k | ~268k | **1.76×** |
| up CPU µs/success | 11.6 | 6.5 | 13.5 | Go lower |

Go's per-request CPU is at or below Rust's on every row and its p50 is at or
below Rust's; the remaining rps gap on the two asset rows is a wall-latency
tail (Go p99 ~0.14 ms vs Rust ~0.06 ms at c=16), not CPU. Disabling the
access log (LOG_REQUESTS=false, same conditions) lifts Go to ~428k avatar /
~480k css — i.e. the access-log formatting+write still costs ~7% of the
avatar row; the async pipeline (above) keeps the line while capping that
cost.

Official harness run (Elixir bench/run, production images, cpus 8-11/12-15,
seed 9f6241dc, HTTP_SECS=4, c=16, SUITES=http, 4 rotating reps; image
`once-campfire-go:engine62` @ cf2bb5b+dfe8310 — the run predates the async
log):

| Route c=16 medians | Go | Rust | vs Rust |
|---|---:|---:|---:|
| room_show | 82,903 | 36,350 | **2.28×** |
| messages_page | 90,234 | 41,583 | **2.17×** |
| sidebar | 54,332 | 35,585 | **1.53×** |
| search | 107,301 | 34,302 | **3.13×** |
| post_message | 8,213 | 6,634 | **1.24×** |
| avatar | 362,768 | 387,913 | 0.94× |
| static_css | 292,898 | 421,961 | 0.69× |
| up | 404,469 | 247,698 | **1.63×** |

Raw: `../once-campfire-elixir/bench/results/engine62-official-20261007/`.
Cell spreads in this run were wide for both apps (e.g. Go avatar
115,660-374,498; Rust avatar 322,407-431,308): the machine carried a
parallel benchmark stream on the same CPU sets and load average 5-8
throughout, crushing individual 4 s windows (the crushed cells hit both apps;
Go's 2-3× p99 tail makes it more sensitive). Manual docker A/B in clean
windows with the access log off: avatar 401-404k, css 459-481k, up 455-462k
— all three above Rust's official median for the same harness conditions
(387,913 / 421,961 / 247,698); with the log on, avatar measured ~352-362k in
clean windows.

Correctness on this tree: mandated full suite `go test -race -count=1
-timeout 800s -p 2 -tags sqlite_fts5 ./...` green (22 packages), the nested
websocket module (`third_party/websocket`) green, vet and gofmt clean. A
pre-existing front-package port flake (`TestServerLoopFlagDiff` freePort
race under `-p 2` with sibling packages binding the same ephemeral pool) is
documented in plans/engine-41.md; it reproduces without this change's route.

## PR review fixes (2026-10-07, PR #10 findings)

Four findings from the pull-request review, each verified against the code and
fixed on `engine`:

1. **`registerRoutes` owns no routes (High).** `internal/engine/router.go`'s
   compiled table is empty, so `CAMPFIRE_ENGINE=on/off/force` are all
   pass-throughs. The runtime behavior is the intended strangler scaffold, but
   the PR description and README overstated it as active route ownership; both
   now describe the scaffold accurately (the measured wins come from the
   handler fast paths). No runtime change.
2. **CreateRoom sidebar bump (Medium).** `internal/database/rooms.go` bumped
   the sidebar version only after a fallible post-commit hydration read; a
   canceled context after commit left a committed room with stale sidebar
   caches. Both registries now move immediately after the commit, before the
   read.
3. **Empty q parameter (Medium).** The single-item `ParseAccept` fast path in
   `internal/httpcompat/formats.go` stripped `q=` only when a value survived
   trailing-empty-field trimming, so `text/html;q=` failed lookup while the
   multi-item path parsed it. The fast path now strips on the presence of a q
   parameter and trims; single-item regression cases added.
4. **Case-insensitive ETag (Medium).** The framed conditional path in
   `internal/web/conditional.go` compared validators with `bytesEqualFold`
   while the map path compared exactly. Both are byte-exact now; a regression
   test pins case sensitivity.

## FINAL5 official acceptance (2026-10-07, async access log; complete HTTP sweep)

Elixir harness, production images, 3 rotating reps, c=16 medians, zero HTTP
errors. Go image `once-campfire-go:final5` (tree 3305f44; the later
review-fix commit `ce6428e` is correctness-only). Every HTTP row is ahead of
Rust, including the auxiliary routes: room 2.63×, search 3.90×, messages
2.45×, sidebar 1.70×, post 1.26×, /up 1.88×, static CSS 1.05×, avatar 1.01×;
CPU per request is below Rust on all eight rows. Cable sustained: 100 clients
1.29×, 500 and 1,000 parity (0.98× / 0.99×); saturated post→all p50 at 1,000
clients 13.5 vs 16.1 ms. Container, ACME and native write/FTS checks pass on
the same image.

## Shared verification harness measurement (2026-10-08, engine-v2 tip e5c5f96)

`basecamp/once-campfire-verification` (revision 7b2dbc7, route-contract-v1
response validation), production image `once-campfire-go:engine-v2`, pinned
Rails seed f5759058, server CPUs 8-11, client 12-15, c=16, 3 rounds of 8 s,
zero contract failures. Medians:

| Route | This fork | Rust (published) | Go upstream (published) | C (published) |
|---|---:|---:|---:|---:|
| room_show | 77,913 | 35,484 | 31,673 | 141,834 |
| messages_page | 91,533 | 40,674 | 30,746 | 151,564 |
| sidebar | 24,430 | 34,479 | 18,586 | 159,850 |
| search | 113,169 | 34,432 | 29,765 | 155,456 |
| post_message | 7,628 | 8,998 | 9,073 | 7,460 |

Against the published shared-harness table: room 2.20× Rust, messages 2.25×,
search 3.29×, post_message 0.85×, sidebar 0.71×. Raw:
`/tmp/opencode/verification-go/summary.json`. The sidebar and post_message
gaps under this referee (both were ahead on the Elixir harness) are the next
targets; profile before changing anything.
