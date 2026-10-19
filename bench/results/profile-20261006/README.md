# CPU profiles — engine ON, five routes (2026-10-06)

Fresh 10-second CPU profiles of the built `campfire` binary (sha256 `e5b0d55b…`,
matches the recorded engine-20261006 run; sources byte-identical to that run's
`go_sources_sha256`). Invocation: `bench/profile <workload> --out
bench/results/profile-20261006 --seconds 10` for room/search/sidebar/write;
`messages` (not a `bench/profile` choice) used the equivalent driver
`bench/results/profile-20261006/route_runner.py` with the same seed copy, env,
affinity (app CPUs 8–11, loadgen 12–15 via `taskset`), concurrency 16, identity
encoding, `GOMAXPROCS=4`, `CAMPFIRE_ENGINE` default (on). All profiles were
non-interfering: one run at a time, background load ~1.0 (ghostty/opencode/
chromium).

Rates reproduced match the recorded medians: room 24,842 rps (rec. 24,519),
messages 25,051 (25,215), search 26,613 (26,865), sidebar 28,699 (26,664,
slightly above the recorded 25,271–27,645 range), write 5,334 (5,399).
All bodies served without errors.

Caveats: profiles sample on-CPU time only. The write route runs ~55 % core-busy
(217.7 % of 400 % sampled); its SQLite commit/fsync waits are off-CPU and are
NOT in the profile's percentages — the write table's DB numbers are a lower
bound of the wall share. Per-route percentages are of that route's total
samples (room 34.26 s, search 35.89 s, sidebar 36.72 s, write 22.24 s, messages
34.28 s), 100 Hz × 4 CPUs.

## Cross-route top flat costs (% of samples)

M=messages, R=room, S=search, B=sidebar, W=write.

| Flat cost | M | R | S | B | W | Meaning |
|---|---:|---:|---:|---:|---:|---|
| `internal/runtime/syscall/linux.Syscall6` | 20.7 | 23.1 | 12.8 | 6.4 | 3.4 | 97 % syscall.write + read — socket I/O, ∝ response bytes (room 374 KB × 24.8k rps ≈ 9.3 GB/s) |
| `runtime.cgocall` | 10.2 | 13.3 | 32.7 | 22.9 | 27.1 | mattn/go-sqlite3 C work (see below) |
| `runtime.tryDeferToSpanScan` (+ GC) | 3.9 | 2.6 | 1.9 | 3.6 | — | allocation pressure (GC) |
| `crypto/fips140/sha256.blockSHANI` | — | — | — | 3.6 | 1.7 | sidebar: fragment-key hash; write: cookie/stream HMAC |
| `reflect.Value.call` | — | — | — | — | 2.1 | template funcs via reflection (markup) |
| `time.parse`/`nextStdChunk` | 2.7 | 2.1 | 1.9 | 0.8 | — | per-message timestamp decode/format |

cgocall callees (single peek per route): reads are dominated by
`_Cfunc__sqlite3_step_internal` (room 9.8 % of samples, search 27.3 %, sidebar
17.5 %, messages 7.1 %) plus `_Cfunc__sqlite3_column_values` (driver column
extraction); write is `_Cfunc__sqlite3_exec_no_args` (13.98 % — BEGIN/COMMIT)
plus step (6.3 %). The C-side step alone is a minority: the mattn driver's
row-materialization (column_values + Go string conversion) and database/sql
glue (`Rows.Next`/`Scan`/`convertAssignRows`) each add comparable time on top of
C `sqlite3_step`.

## Room (`GET /rooms/486777696`)

Top 8 flat: Syscall6 23.1 %, cgocall 13.3 %, tryDeferToSpanScan 2.6 %,
Mutex.Unlock 1.4 %, gcmarknewobject 1.4 %, time.parse 1.2 %, atomic CAS 1.0 %,
memmove 1.0 %.
Top 8 cumulative: conn.serve 87.7 %, engine.ServeHTTP 83.4 %,
front.Deflate 83.4 %, Server.ServeHTTP 83.2 %, routeHTTP 53.0 %,
router.ServeHTTP 51.4 %, auth.func1 49.7 %, `Server.room` 38.3 %.

| Cost | Share | file:line |
|---|---:|---|
| Response socket write (syscall.write chain) | 23.1 | net/http `response.write` → `poll.(*FD).Write` → Syscall6 (room `Server.ServeHTTP.func1` 26.2 cum, `responseBuffer.finish` response.go:61) |
| DB: `MessagePageReferences` (page of 40) | 19.5 cum | database/messages.go:316 (Rows.Next 17.9, Scan 10.9, convertAssign 5.0, QueryContext 4.9, close 4.8) |
| `render` (shell template + Account read) | 8.3 cum | server.go:376 (Account at 380) |
| auth/session (auth 49.7 − room 38.3) | 11.4 | `SessionUser` models.go:74, `RefreshSession` accounts.go:261, `VerifyCookie` rails/cookies.go:158, `blockBrowser` |
| GC + allocation | ~9–12 | mallocgc 6.9, gcBgMarkWorker 6.5, scanObject 2.8, tryDeferToSpanScan 3.6 |
| DB: `Room` read | 2.9 | rooms.go `Room` + Scan 6.6 (displayRoom) |
| `SignStream` (Turbo-stream HMAC) | 1.2 | rails/streams.go:16 |
| `rememberRoom` (cookie set) | 0.8 | session.go:162 |
| bufio Flush | 4.5 | net/http chunked flush |

Piece path works as designed: `recordedMessageList` (recorded.go:64) hit path is
a digest lookup — no payload re-hash, no recompression; `writeRecordedPieces`
(recorded_pieces.go:346) assembles from cached segments. What remains is the DB
page read (19.5 %), the JS-shell render (8.3 %, legacy template engine), auth's
two extra SELECTs, and the unavoidable 23 % socket-write share at 9.3 GB/s.

## Messages (`GET /rooms/486777696/messages?before=933434569`)

Top 8 flat: Syscall6 20.7 %, cgocall 10.2 %, tryDeferToSpanScan 3.9 %,
gcmarknewobject 1.8 %, time.parse 1.4 %, time.nextStdChunk 1.3 %,
typePointers.next 1.3 %, Mutex.Unlock 1.1 %.
Top 8 cumulative: conn.serve 86.4 %, engine 82.3 %, Deflate 82.2 %, Server 82.1 %,
routeHTTP 54.4 %, router 52.7 %, auth.func1 50.7 %, `Server.messages` 39.7 %.

| Cost | Share | file:line |
|---|---:|---|
| Response socket write | 20.7 | (342 KB body, 25.1k rps ≈ 8.6 GB/s) |
| DB: `MessagePageReferences` | 19.7 cum | messages.go:316 (Rows.Next 15.0, Scan 6.9, convertAssign 4.9, `timestamp.Scan` 4.0 + `time.Parse` 3.9) |
| `messageFreshness` ETag rebuild | 8.1 cum | conditional.go:39 — per request: 40× (Sprintf + `Format("20060102150405.000000")` + ReplaceAll), Join, sha256, etag Sprintf, 3 header sets. Children: time.Format 27.6 %, Sprintf 25.8 %, ReplaceAll 15.4 %, sha256 7.9 %, convT64/convTstring 9.3 % |
| `render` | 7.6 cum | server.go:376 |
| auth/session | 11.0 | same as room; auth children: SessionUser 7.6 % of auth, VerifyCookie 6.3 %, RefreshSession 4.0 %, blockBrowser 1.9 % |
| DB: `Room` + Scan | 5.7 cum | rooms.go |
| GC + allocation | ~10–13 | mallocgc 9.5, gcBgMarkWorker 7.1, scanObject 3.7, tryDeferToSpanScan 5.0 |

## Search (`GET /searches?q=coffee`)

Top 8 flat: cgocall 32.7 %, Syscall6 12.8 %, tryDeferToSpanScan 1.9 %,
atomic CAS 1.2 %, time.parse 1.1 %, Mutex.Unlock 1.0 %, regexp.FindStringSubmatch 0.9 %,
memmove 0.8 %.
Top 8 cumulative: conn.serve 91.7 %, engine 86.4 %, Deflate 86.4 %, Server 86.3 %,
routeHTTP 68.9 %, router 67.9 %, auth.func1 65.8 %, `Server.search` 54.5 %.
(9th: `database/sql.(*Rows).Next` 38.4 %.)

| Cost | Share | file:line |
|---|---:|---|
| DB: `Search` — FTS + row scan | 28.1 cum | models.go:279 (`Search`), `scanMessages` 27.1; C `sqlite3_step_internal` alone 27.3 |
| `render` | 12.4 cum | server.go:376 |
| Response socket write | 12.8 | 135 KB body |
| DB: `Rooms` (sidebar room list for the page) | 9.9 cum | database rooms.go `Rooms` (inline 9.9) |
| DB: `RecentSearches` | 6.4 cum | search.go:42 |
| GC + allocation | ~6–9 | mallocgc 5.8, … |

## Sidebar (`GET /users/me/sidebar`)

Top 8 flat: cgocall 22.9 %, Syscall6 6.4 %, sha256.blockSHANI 3.6 %,
tryDeferToSpanScan 3.6 %, Mutex.Unlock 1.4 %, nanotime 1.4 %, memmove 1.1 %,
scanObject 1.0 %.
Top 8 cumulative: conn.serve 86.2 %, engine 80.6 %, Server 80.5 %, routeHTTP 65.4 %,
router 63.8 %, auth.func1 61.9 %, `Server.sidebar` 51.2 %, `Rows.Next` 30.8 %.
(9th: `Server.sidebarRooms` 24.7 %.)

| Cost | Share | file:line |
|---|---:|---|
| DB: `sidebarRooms` subtree | 24.7 cum | sidebar.go:75 → `SidebarRooms` (rooms.go:286) 11.2, `RoomMembers` (accounts.go:291, via `displayRoom` sidebar.go:45–48, one per direct room) 12.3 |
| DB: `DirectPlaceholders` | 16.1 cum | accounts.go:299 (usersRows scan 16.6 cuм includes it) |
| `render` (sidebar template) | 8.8 cum | server.go:376 |
| Fragment-key construction + sha256 | 3.6 flat | sidebar_cache.go:13–31 — per request: ~15 `fmt.Fprintf`s + `sha256.Sum256`; 97 % of sha256 flat here (cookie HMAC is the rest) |
| Response socket write | 6.4 | 9.4 KB body |
| auth/session | 10.7 | auth 61.9 − sidebar 51.2 |
| GC + allocation | ~10–12 | mallocgc 7.7, gcBgMarkWorker 7.3, scanObject 3.5, tryDeferToSpanScan 4.7 |
| `SignStream` | 1.4 | streams.go:16 |

Sidebar is a 9.4 KB fragment yet spends ~57 % of samples before the fragment is
served: every request re-runs the full membership/placeholder DB reads, rebuilds
and re-hashes the fragment key, then (re)renders nothing (cache hit) but the
template engine still does the `render` pageSetup work. `internal/fastdb`
already implements `SidebarRooms`/`RoomMembers` (fastdb.go:424, 359; same SQL,
caller-owned dst, 0 allocs/op measured) but is not wired into web; no fastdb
`DirectPlaceholders` exists yet.

## Post message (`POST /rooms/201306877/messages`)

Top 8 flat: cgocall 27.1 %, Syscall6 3.4 %, reflect.Value.call 2.1 %,
memmove 2.0 %, sha256.blockSHANI 1.7 %, regexp.tryBacktrack 1.5 %,
Mutex.Unlock 1.0 %, `reflect.(*structType).FieldByNameFunc` 0.9 %.
Top 8 cumulative: conn.serve 87.7 %, serverHandler 85.6 %, engine 85.6 %,
Deflate 85.6 %, Server 85.3 %, routeHTTP 76.0 %, router 75.8 %, auth.func1 74.8 %.
(9th: `Server.createMessage` 69.6 %.)

Request split (of 22.24 s samples; route is off-CPU bound → wall shares of the
commit are higher than these CPU shares):

| Cost | CPU share | file:line |
|---|---:|---|
| `createMessage` total | 69.6 cum | server.go:831 |
| — `messageViews` | 40.2 cum | messages.go:60 — markup template 25.2 (messages.go:143; `safeCall` → `reflect.Value.Call` 12.9), richtext.process/Display ~8 (internal/richtext), DB FindRoom/User per view ~5 |
| — `saveNewMessage` → DB `createMessage` | 20.3 cum | message_attributes.go:92 → models.go:209 — `Transaction` 19.1, `database/sql.withLock` 18.1 (single-writer mutex), `SQLiteTx.Commit` 14.2 (`exec_no_args`: COMMIT), step 6.3 (INSERTs: message, FTS row, unread) |
| — `messageCreated` (stream render + publish) | 5.8 cum | push.go:193 |
| — DB `Room` | 3.2 cum | rooms.go |
| auth/session (auth 74.8 − createMessage 69.6) | 5.2 | VerifyCookie incl. its sha256/HMAC; bcrypt appears once per run (login), 0.8 |
| response write (`ServeHTTP.func1`/`finish`) | 5.4 cum | response.go:61 (turbo-stream body, 8,380 B) |
| broadcast/Cable publish | ~0.1 | `cable.Hub.Publish` (inline) 0.07 — CPU-cheap in-memory fanout; wall cost not sampled |
| webhook-job DNS | 0.5 flat | `net._C2func_getaddrinfo` (deliverWebhook's HTTP dial, webhooks.go:54+) — one cgo DNS lookup per message |

## DB share and the fastdb replacement surface

Measured DB shares (cum, % of samples): room ~22.4 (MessagePageReferences 19.5 +
Room 2.9), messages ~25.4 (19.7 + 5.7), search ~44.4 (Search 28.1 + Rooms 9.9 +
RecentSearches 6.4), sidebar ~40.8 (sidebarRooms subtree 24.7 + DirectPlaceholders
16.1), write ~23.5 (createMessage DB 20.3 + Room 3.2). Auth adds two more
roundtrips per request on every route: `SessionUser` (models.go:74) and
`RefreshSession`'s SELECT (accounts.go:261) — about 60 % of the auth overhead
(SessionUser + RefreshSession ≈ 5.9 pp on messages; VerifyCookie ≈ 3.2 pp).

fastdb (`OpenReadOnly`) mirrors every hot read used on these paths — `Room`,
`RoomMembers`, `SessionUser`, `SidebarRooms`, `MessagePageReferences`
(fastdb.go:249/359/392/424/526) with cached statements, positional columns,
typed timestamp decode and caller-owned dst (0 allocs/op; measured ref-scan
11.4 µs/40 rows); `DirectPlaceholders` is not yet mirrored. The wrapper cost
above the C step — mattn row materialization (column_values + per-row string
conversion) plus database/sql glue (nextLocked/Scan/convertAssign/retry/close,
~15–25 % of the DB cum totals) — is what fastdb removes; the C
`sqlite3_step`/`column_*` time stays. Wiring fastdb in removes ≈5 pp (room),
≈7 pp (messages), ≈10 pp (search), ≈8 pp (sidebar) of sampled CPU on the read
routes, before any query reduction.

## Allocation evidence

Live-server heap profiles are not possible without code changes (server honours
only `GO_CPU_PROFILE`); GC/alloc shares above were read from the CPU profiles.
Attribution comes from `go test -bench BenchmarkRecorded* -memprofile`
(internal/web/recorded_bench_test.go, `-tags sqlite_fts5`):

- Recorded hit path (full 376 KB room page, cache hit, in-process):
  identity 2,193 ns/op, 376 B/op, **9 allocs/op**; gzip 2,595 ns/op, 484 B/op.
- alloc_objects sites (sample_index=alloc_objects): `MIMEHeader.Set` (inline)
  63.9 % — per-request header sets in `writeRecordedPieces`/conditional; `weakETag`
  10.5 %; `responseBuffer.finish` 28.4 cum (parts slice); sha256 `Digest.Sum` 0.5 %.
  The 9 allocs/op ≈ 6 header-map allocs + ETag + parts slice + one misc.
- Room/messages carry a second allocation source the bench does not see:
  `messageFreshness`'s per-message Sprintf/Format/ReplaceAll/convT sites
  (conditional.go:42–47) ≈ 8 % of messages samples, and database/sql
  `convertAssignRows` + per-row string allocs on every row scanned.

## perf stat (instructions:u, cycles:u, app process only)

`perf stat -p <server> -e instructions:u,cycles:u sleep 12` around the loadgen
window (12 s incl. login/startup; loadgen ran the full 10 s):

| Route | instructions | cycles | IPC | instr/request |
|---|---:|---:|---:|---:|
| messages (25,051 rps × 10 s) | 355.7 G | 133.0 G | 2.68 | ≈1.40 M |
| room (24,843 rps × 10 s) | 308.6 G | 127.8 G | 2.41 | ≈1.24 M |

(The 12 s window includes ~1–2 s of pre-load; instr/request is an upper bound.)

## Next three optimizations per route (ranked, magnitude, risk)

**room_show**
1. Engine-owned room route (ENGINE-16 design): fastdb positional reads for the
   seven roundtrips (Room, MessagePageReferences, RoomMembers, invitation,
   Account, SessionUser, RefreshSession) + versioned auth fast path.
   Magnitude: +15–25 % (removes most of the 22 % DB share and its allocs).
   Risk: high — strangler migration; differential harness must stay green;
   RefreshSession/Auth semantics are subtle.
2. Kill `messageFreshness`-style ETag rebuild… room already uses piece digests;
   instead: cache `MessagePageReferences` per (room, room-version) in the
   piececache keyed off room version, so the page read becomes version-check +
   digest lookup. Magnitude: +15–20 % (nearly all of the 19.5 % read vanishes
   on the hot path). Risk: medium — version-key correctness (mutation fuzz;
   message edits/deletes/boosts must bump).
3. Per-request arena for page assembly: fold the hit-path header sets and parts
   slice into one arena so the 9 allocs/op → ~1. Magnitude: +3–6 % (GC share is
   ～9–12 %). Risk: low — contained in `writeRecordedPieces`/`responseBuffer`.

**messages_page**
1. Cache the message-list piece by (room, room-version, anchor-page) — same idea
   as room #2; today every request re-reads 40 rows through cgo for a page that
   changes only on new messages/edits. Magnitude: +15–20 %. Risk: medium
   (versioning, first-page vs before-anchor variants).
2. Delete `messageFreshness`'s per-request rebuild (conditional.go:39): derive
   the ETag from the cached piece digest + loadedAt instead of re-formatting and
   re-hashing all 40 messages. Magnitude: +6–8 % (its 8.1 % plus allocs).
   Risk: low — digest already covers the content; keep 304 semantics.
3. Same auth fast path as room (one SELECT instead of SessionUser +
   RefreshSession SELECT + cookie HMAC per request). Magnitude: +5–8 %.
   Risk: medium — session-state semantics (RefreshSession is a wall-clock-hour
   gate; needs an in-memory version cache).

**search**
1. fastdb FTS positional reads for `Search` (ENGINE-30 plan): replaces the
   28.1 % scan's glue + row materialization; ~30 % of that share disappears.
   Magnitude: +8–12 %. Risk: medium — FTS5 via csqlite needs the FTS5 C API
   path; result-piece cache must invalidate on message mutations.
2. Bound + cache result pieces by (query, corpus-version) so repeat `q=coffee`
   traffic skips the scan entirely. Magnitude: +15–25 % on repeated-query
   traffic (loadgen is one query). Risk: medium — poisoning/fuzz coverage
   (plan §ENGINE-30) required; query normalization must match SearchQuery.
3. `Rooms` read (9.9 %) → fastdb (exists: `rooms` needs adding) or reuse the
   sidebar fragment's room data. Magnitude: +4–6 %. Risk: medium.

**sidebar**
1. Wire fastdb `SidebarRooms` + `RoomMembers` (already implemented; add
   `DirectPlaceholders`) into the sidebar read; one JOIN instead of four
   queries incl. per-direct-room RoomMembers. Magnitude: +10–15 %. Risk: medium
   — Differential tests exist (fastdb/differential_test.go) but web wiring is
   new; unread/involvement columns must match.
2. Cache the rendered fragment by a *version tuple* from
   `SidebarRooms`/`DirectPlaceholders` rows (per-room updated_at + membership
   version) instead of re-reading all rows and re-hashing the key string
   (sidebar_cache.go:13, 3.6 % sha256 + key Fprintfs). Magnitude: +12–18 %
   (reads 40.8 % + key construction). Risk: medium — cache-key audit is the
   correctness surface (any field the template reads must be in the tuple).
3. Replace the legacy template render (8.8 %) with a compiled renderer only on
   the fragment miss; the hit path should skip `render`'s pageSetup (Account
   read) entirely. Magnitude: +4–6 %. Risk: low–medium.

**post_message**
1. Single immediate transaction with `PRAGMA synchronous=NORMAL`-equivalent
   durability trade evaluated (commit is 14.2 % of CPU samples and the route is
   off-CPU bound at ~55 % busy — commit/fsync is the wall bottleneck: median
   1.8 ms). Magnitude: +20–40 % throughput. Risk: high — durability contract;
   must stay within the reference's WAL/durability semantics; measure before
   claiming.
2. `messageViews` (40.2 %): the markup template cost (25.2 % incl. 12.9 %
   reflection calls) → compiled fragment renderer shared by the Turbo response
   and cable publish (ENGINE-31 plan: fastrender). Magnitude: +10–15 %.
   Risk: medium — byte-identical fragment output is the gate.
3. Reduce per-message DB roundtrips in `messageViews` (FindRoom per distinct
   room, User per distinct creator) via fastdb reads with warmed conns, and
   drop the per-message webhook job's DNS (getaddrinfo 0.5 %) by dialing once
   per bot with connection reuse. Magnitude: +3–6 %. Risk: low.

## Artifacts

`bench/results/profile-20261006/`: `room.pprof` (perf-attached run, 24,843 rps —
overwrote the identical bench/profile run), `search.pprof` (26,613),
`sidebar.pprof` (28,699), `write.pprof` (5,334), `messages.pprof` (25,051),
`*.json` loadgen results, `room.perfstat`, `messages.perfstat`, `route_runner.py`
(driver, also serves messages_page), this README. `recorded.memprof`
(alloc_objects) and raw `-top -cum` dumps live in the ignored `tmp/`/`/tmp`.
No tracked file was modified; `web.test` (stray `go test` binary left at the
repo root by the bench runs) was removed.