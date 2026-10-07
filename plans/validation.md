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
