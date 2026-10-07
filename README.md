# once-campfire-go

Campfire in Go, ported from [ONCE Campfire in Rust](https://github.com/basecamp/once-campfire-rust).
It uses the Go standard library for routing, HTTP, templates, SQL access, cryptography and process
lifecycle. There is no web framework, ORM, dependency injection container or frontend framework added
by the port. The original Turbo/Stimulus/Lexxy frontend is retained.

The application includes setup and invitations, sign-in and session transfers, account and user
administration, open/closed/direct rooms, messages and attachments, editing/deletion, boosts,
mentions, search, notifications, bot APIs/webhooks, link previews, Web Push, and Action Cable
presence, typing, read/unread and live updates. It includes media processing, the public HTTP/TLS
front server, automatic certificates, production packaging and backup/restore hooks.

SQLite schema, storage keys/layout, password hashes and Rails signed/encrypted cookies are preserved.
An upgrade test checks Go and Rust cookies in both directions and Rust reading/searching Go-written
messages. The port is functionally implemented; **strict HTML/network parity is not complete**.
See [validation](plans/validation.md) and the known differences below before replacing an installation.
The pinned Rust source is in `reference/`, with its Rails source in `reference/reference/`.

## Dependencies

- Go 1.27.1: `net/http`, `html/template`, `database/sql`, `crypto`, `encoding/json`, `embed`, `testing`.
- `github.com/mattn/go-sqlite3`: SQLite with FTS5, through CGO.
- `github.com/coder/websocket`: WebSocket transport; the Action Cable protocol and channels are local code.
- `golang.org/x/crypto`: bcrypt and ACME. The pinned revision includes the upstream ACME missing-Location fix.
- `golang.org/x/net`: HTTP/2 and HTML tokenization; a local tree-builder fork matches the reference parser.
- `golang.org/x/text`: indirect Unicode support.
- Native libvips, ffmpeg/ffprobe and libzstd: image/video processing and public HTTP zstd compression.

A local build needs a C compiler, pkg-config, libvips and libzstd development headers, ffmpeg, and
Python 3 for asset generation. Assets are built from the pinned sources without Ruby, Rust or Node.
The Dockerfile pins the reference media toolchain: libvips 8.16.1 and ffmpeg 7.1.5.

## Run

```sh
git submodule update --init --recursive
bin/build
export SECRET_KEY_BASE="$(openssl rand -hex 64)"
DISABLE_SSL=1 HTTP_PORT=8080 ./campfire server
```

Open <http://localhost:8080/first_run>. Keep the same `SECRET_KEY_BASE` across restarts; use an
existing installation's secret to retain logins when migrating its database and files.

```sh
docker build -t once-campfire-go .
docker run --rm -p 8080:80 -e DISABLE_SSL=1 -e SECRET_KEY_BASE \
  -v campfire-go-storage:/rails/storage once-campfire-go
```

The container runs as uid/gid 1000. For a bind mount, make its storage writable by that user.
`CAMPFIRE_STORAGE_PATH` defaults to `storage`, with databases in `db/`, media in `files/`, backups
in `backups/`, and certificate cache in `thruster/`. `CAMPFIRE_DATABASE_PATH` and `CAMPFIRE_FILES_PATH`
override individual locations; `RAILS_ENV` defaults to `production`. `CAMPFIRE_ENGINE` selects the
mode of the strangler scaffold in front of the legacy handler: the compiled route table currently owns
no routes, so `on` (default), `off` and `force` are all pass-throughs and serve every request through
the legacy chain unchanged. The switch exists for the differential harness and for promoting routes
one at a time; the measured performance comes from the fast paths inside the handlers, not from route
ownership.
`CAMPFIRE_FRAGMENT_CACHE_MB` sizes the legacy in-process HTML fragment cache (default 32; `0`
disables caching), which holds per-message fragments, the sidebar and the legacy recorded shells
when the piece path is off; the engine piece cache reads the same key when routes migrate to it, so
with both full the worst case is 2× the configured MiB (64 MiB by default), kept intentionally
during the strangler migration. `CAMPFIRE_CABLE_FAST` (default `on`) gates the Action Cable
fan-out fast paths: per-wake batched/vectored writes (ENGINE-40), the exact-payload frame
cache (ENGINE-40), and the versioned publication authorization cache with lock-free fan-out
(ENGINE-40b); `off` selects the legacy one-write-per-frame path with a per-publish
auth-query for A/B. Under the fast path the hub authorizes a broadcast against an in-memory
cache keyed by (room, session token) plus the (session, membership, user-status)
generations from `internal/database/versions.go`, so a steady-state publish runs no
authorization SQL at all; a cached result never outlives its generations — session writes
(login/logout/ban/deactivation), membership and room-visibility writes, and bans all bump a
generation after commit, and the next publication re-queries and excludes the revoked
client. Recipients are snapshotted under the hub read lock into a pre-sized slice and
authorized/delivered without the lock. Deliberate difference, the same single-process limit
as the sidebar/search caches: authorization changed by SQL outside the audited write
helpers (other processes, direct writes) is picked up on restart or at the next generation
bump, not live. The write-helper audit is pinned in `internal/database/versions_test.go`.
Logout goes through `DB.DeleteSession` so the session deletion bumps the generation before
the caller touches the hub. Sidebar fragments are keyed by a version, not by their content: an
in-process registry (seeded once from the database on first use; bumped after every sidebar-visible
write — room create/rename/delete, membership join/leave/removal, unread changes via message
create and presence, user rename/avatar/role, direct-placeholder transitions, account room
restrictions) yields the key without reading any room, membership or placeholder rows, so a cache
hit serves the fragment with no row reads and no page setup, and the entry accounting is the same
bounded LRU as every other fragment. The registry is per process: writes from another process are
picked up on restart, not live (the same documented limit as the message-fragment caches). The
write-helper audit is pinned in `internal/database/versions_test.go`, which fails when a
sidebar-visible write helper stops bumping the version. No runtime gate backs the version keys:
the before/after A/B compares binaries (`bench/application --baseline-go`), so a gate would only
add a second code path to keep byte-identical. `CAMPFIRE_RECORDED_PIECES` (default `on`; also accepts `true`/`1`
and `off`/`false`/`0`, warning on anything else) serves recorded room, messages and search pages
from cached compressed pieces: the shell split at its `loadedAt` and message markers (`layout`,
`0`..`2`) and the message list, each keyed by a SHA-256 of the rendered page inputs or of the
message ids and updated-at stamps and stored as raw bytes plus a raw DEFLATE fragment (a
block-stream ended at a byte-aligned non-final boundary by a Flush, at `CAMPFIRE_RECORDED_GZIP_LEVEL`,
default 9). Gzip clients receive ONE gzip member spliced from those fragments — header,
fragments, final empty stored block and a single CRC32/ISIZE trailer of the whole body, with the
per-piece CRCs combined in GF(2) at assembly time so no raw byte is re-scanned — because
Chromium decodes only the first member of a multi-member gzip stream and rendered an empty
message list on the old multi-member wire. The splice is the browser-safe form; identity
clients still get the raw pieces with no copy. Setting it
`off` restores the legacy per-request render, compression and ETag path for A/B and rollback.
`CAMPFIRE_RECORDED_ZSTD` (default `off`; same value shapes and warning policy) enables the zstd
frame variant of cached pieces. It is off by default for the same browser reason: Chromium
decodes only the first frame of a multi-frame zstd stream, and every piece-path page assembles
several pieces, so with the flag on, multi-piece responses are served as the single-member gzip
splice and only single-frame shapes would use zstd; the multi-frame zstd assembly remains in
`internal/piececache` for non-browser clients and the corpus that exercises it.
`CAMPFIRE_FASTDB` (default `on`; also accepts `true`/`1` and `off`/`false`/`0`, warning on anything else)
routes the hot read paths — room pages, message pages, the session lookup in `auth`, the sidebar
(rooms, direct-room members, placeholders), the search FTS scan, and message view assembly (room,
creator, boosts and attachment per view) — through `internal/fastdb`, a thin
SQLite read layer that scans with caller-owned buffers and no per-record database/sql decoding,
holding one pooled read-only connection per application CPU (`CAMPFIRE_FASTDB_POOL_SIZE` overrides the
size; the startup log line reports it). Setting it `off` restores the
`database/sql` readers on the same handlers for A/B and rollback; `GET /searches` itself still reads
recent searches through `database/sql` unless the result cache (below) serves the page, and every
write (including the hourly `RefreshSession` UPDATE) always uses `database/sql`, and the profile and
bots pages keep their user-list reads on `database/sql` by design, so the wired-path list above is
accurate otherwise. If the pool cannot
open (unreadable database, non-WAL file), the server logs a warning and falls back to `database/sql`
reads for that process instead of failing startup. The fast path also does not observe request
cancellation mid-query; a cancelled request completes its read and renders rather than producing a
context error.
`CAMPFIRE_SEARCH_CACHE` (default `on`; same value shapes and warning policy as the other switches)
serves repeated `GET /searches` pages from a bounded in-process result cache keyed by (user,
normalized query, corpus version, membership version), skipping the FTS scan, the `Rooms` read and
the `RecentSearches` read on a hit; the message fragments come from the recorded piece cache (same
content-keyed pieces a miss would produce), so no fragment re-renders. The corpus version is a
global counter bumped by every committed message create/edit/delete, boost change and room destroy;
the membership version is bumped by every committed membership grant/revocation, involvement change
and account create/deactivate. A hit therefore serves the byte-identical page a re-read would
produce — the differential and poisoning tests hold it that way, including a test that drops the FTS
index after a fill and requires the cached path to keep serving while `database/sql` fails. POST
`/searches` and `DELETE /searches/clear` keep their database behavior and also purge the user's
cached entries, so the rendered recent-searches list stays current. Deliberate trade-offs: on a hit
the page's "last room" link is still read per request (it depends on the `last_room` cookie), and a
cached entry can be as stale as the version counters allow — a version bump is the only invalidator,
so a process whose writers run outside `internal/database` (none today) would serve stale pages.
`CAMPFIRE_SEARCH_CACHE_MB` sizes it (default 16; `0` disables storage; an entry larger than a
quarter of the budget is not cached).
`CAMPFIRE_WRITE_QUEUE` (default `on`; also accepts `true`/`1` and `off`/`false`/`0`, warning on
anything else) routes message creation through a single writer goroutine that commits everything
queued at a drain in one `BEGIN IMMEDIATE` transaction (group commit), each job in its own savepoint
so a failing job rolls back exactly its own statements while the batch commits. Each request learns
its message only after the shared commit — the response is never sent ahead of persistence — and
then runs the search-index insert and unread bump itself, after the commit, the way the Rust port's
`after_commit` hooks shape the same Rails callbacks. The writer connection disables SQLite's
auto-checkpoint (`PRAGMA wal_autocheckpoint=0`) and a separate connection runs
`PRAGMA wal_checkpoint(PASSIVE)` every `CAMPFIRE_CHECKPOINT_MS` milliseconds (default 1000), so no
write ever waits for the checkpoint's WAL and database fsyncs. Durability does not move: the
database stays WAL mode with `synchronous=NORMAL` and the default `journal_size_limit` (unset,
-1), commits are never fsynced, and what committed since the last checkpoint can be lost to a power
failure (never to a process crash); every checkpoint syncs the WAL, as the auto-checkpoint did. Two
deliberate windows follow from the after-commit shape: a crash (or a failed statement) between the
shared commit and the search/unread statements leaves the message row without its index row, and a
request that disconnects after its job started still persists (the job's context is only checked
before execution). Setting `off` restores the per-request transaction path with the message, index
and unread bump in one rollback-atomic transaction and the writer's auto-checkpoint, byte-for-byte
the pre-lane behaviour; the checksum of the response bytes is covered by the write-lane parity
tests. The sidebar and search-cache version counters move with the shared commit on both paths
(group-commit and per-request), so the write lane never serves a version-keyed cache that hides a
committed message.
`CAMPFIRE_FASTDB_WRITE` (default `on`; accepts the same value shapes and warning policy as
`CAMPFIRE_WRITE_QUEUE`) runs the message create transaction as direct prepared statements on one
serialized fastdb connection (`internal/fastdb/write` over `internal/fastdb/csqlite`) instead of
`database/sql`: the same eight statements — membership check, creator name, message insert, room
touch, rich-text body, attachment, then the search-index insert and unread bump in
after-commit order — bound positionally (int64/text/null), prepared once and reused across jobs,
with the same `BEGIN IMMEDIATE`, savepoint-per-job, single-commit-per-batch and
after-commit-tx-of-its-own shapes the `database/sql` lane has. The pragma set is identical
(`busy_timeout=5000`, `foreign_keys=on`, WAL, `synchronous=NORMAL`, `cache_size=2000`,
`wal_autocheckpoint=0` with the off-writer PASSIVE checkpointer unchanged), and the error
mapping matches (`errors.Is` against the same sentinels: `sql.ErrNoRows`, the csqlite
constraint/busy codes; only the error text formatting differs). The statement-count tests pin
the per-post text/order/count on both lanes: 12 counted statements with the flag off, 5 with it
on (the create transaction's seven statements leave the counted driver), and the direct lane's
recorded stream is pinned by text and order in `internal/database/fastwrite_test.go`. The
twin-database differential test drives identical create workloads (plain, attachment, staged
upload, webhook, queued, and the forbidden/no-creator/FK failure cases) through both lanes and
compares every touched row byte-for-byte. The flag only affects the queued lane: with
`CAMPFIRE_WRITE_QUEUE=off` the per-request transaction path stays `database/sql`, exactly as
before.
`CAMPFIRE_AUTH_FAST` (default `on`; same value shapes and warning on anything else) is the
auth/session fast path: a bounded in-process cache of verified `session_token` cookie values keeps
their token and signed expiry, so a previously verified cookie is not re-HMACed and re-decoded on
every request — the cache re-checks the signed expiry against the current time on every request, is
keyed by the signing-key generation (a rolled secret is never served from a cache populated under
the old one), and only a full verification ever inserts an entry, so tampered, malformed, expired,
revoked and banned sessions are rejected exactly as on the legacy path. The fast path also replaces
the per-request `SessionUser` + `RefreshSession` SELECT pair with one joined fastdb read carrying
`last_active_at`; the hourly `RefreshSession` write and the re-signed cookie still run through
`database/sql` unchanged, gated in Go against that stamp, so a session is refreshed (and its cookie
re-written) at most hourly — write-only-when-changed. Setting it `off` restores the legacy
per-request full verification and two-step read for A/B and rollback; the engine pool still serves
the other fastdb reads. One deliberate difference: on the legacy path a session deleted mid-request
(between the two reads) surfaces as a 500 from `RefreshSession`; the joined read sees the deletion
and redirects to sign-in instead.
`CAMPFIRE_COMPILED_ROUTES` (default `on`; also accepts `true`/`1` and `off`/`false`/`0`, warning on
anything else) matches every request against a segment-wise compiled form of the 177-entry Rails
route contract (routes grouped by method and first literal segment; byte compares and scans, no
regex execution per request) instead of the per-route regex scan. The compiled recognizer must
produce byte-identical recognitions — same contract, same path values, same errors — and a
deterministic differential corpus (reference vectors, generated per-route paths, hostile edge
paths, seeded fuzz) pins that in `route_compiled_test.go`. Setting it `off` restores the regex
recognizer for A/B and rollback; `routeHTTP` and every handler are untouched. Patterns whose shape
the compiler cannot prove identical to the regex semantics (no such pattern exists in the current
table: any `( )` group other than the trailing `(.:format)`, a param followed by literal text in
the same segment, a non-final star, a dot in the first segment) disable the compiled table for the
whole process — always the regex answer, never a wrong compiled one. Paths containing a raw
newline also take the regex recognizer, because `.` and `[^...]` exclude `\n` but the compiled
scans would not.
`CAMPFIRE_RECORDED_CACHE_MB` sizes that recorded-response piece cache independently (default 32;
`0` disables storage while still serving from freshly rendered pieces). Each cached piece charges
its raw bytes, its deflate fragment and the key, so one piece costs up to about twice its HTML size;
with the fragment and recorded caches full the accounting is 64 MiB by default. The recorded room
page's ETag covers the cache-stable pieces only, so it no longer moves with the per-request
`loadedAt` timestamp — a deliberate difference from the legacy per-request hash, which would
re-hash the shell on every request. `CAMPFIRE_MESSAGE_REFS_CACHE_MB` sizes the message-page
reference cache (default 8; `0` disables it, restoring the per-request 40-row scan): the room and
messages pages both re-run the same `MessagePageReferences` scan on every request, so its result —
the reference list (message id, room, update stamp) plus the messages-page validator (ETag,
Last-Modified, Cache-Control, both Turbo-Frame variants, byte-identical to the former per-request
rebuild) — is cached keyed by `(room, room updated_at, anchor, direction)` and served warm at one
in-memory lookup with no scan (measured 48 ns/op, 0 allocs, vs 20.8 µs/op on the scan path).
Validation is the key itself: every message create/edit/delete/boost writes `rooms.updated_at`, so
a changed room version misses and refills. A hit returns only references; full message rows (rich
text, author, boosts) hydrate exactly where they always did, on a per-message fragment cache miss,
which also keeps the around/after pages on the same hydration path the before pages always used.
The cache is bounded by bytes and by 4096 entries, pruned oldest-first to 75% of the budget like
the fragment cache; a single window never exceeds a quarter of the budget.
`CAMPFIRE_FAST_RENDER` (default `on`; also accepts `true`/`1` and `off`/`false`/`0`, warning on anything else) compiles the `message-uncached` fragment — with
its `message-actions`, `presentation`, `boosts` and `boost` partials — once at startup into a flat
program of literal and field ops (`internal/web/fastrender.go`): the compiled renderer appends the
same bytes `html/template` would produce (it is compiled from the templates' own escaped
pipelines and re-implements exactly those escapers) into a caller-supplied buffer with zero
allocations of its own, and `messageViews` shares that fragment with the cable broadcast and the
Turbo response without re-rendering. Setting it `off` reverts to executing the fragment through
`html/template`, byte-identically, for A/B and rollback. If the fragment compile fails (a template
edit introduced a construct it does not know), the server logs a warning and keeps serving via
`html/template` rather than failing startup. `campfire db:prepare` initializes an empty database and checks
migration versions; existing databases missing migrations are rejected.

The Go runtime's garbage collector is exposed through `CAMPFIRE_GOGC` (the GOGC target percentage,
e.g. `200`, or `off` to disable the collector) and `CAMPFIRE_GOMEMLIMIT` (a soft memory limit in
bytes, e.g. `536870912`, or with a binary `MiB`/`GiB` suffix, e.g. `512MiB`), both applied at
startup and logged with the effective values. An absent knob leaves the runtime's own behavior
alone — Go already reads `GOGC` and `GOMEMLIMIT` from the environment itself — and an invalid
value logs a warning and keeps the runtime setting. Read profiles show GC at roughly 9–12% of
samples on read routes; the task is to drive that share down without raising peak memory, and the
defaults the shipped image lands on are recorded with the ENGINE-47 measurement (this text stays
knob documentation until that evidence exists).

The public listener uses `HTTP_PORT=80`. Set `TLS_DOMAIN` for automatic ACME certificates and HTTPS
on `HTTPS_PORT=443`. The internal application listener defaults to `TARGET_BIND=127.0.0.1` and
`TARGET_PORT=3000`. The front server provides HTTP/2, optional H2C, gzip/zstd with compression jitter,
a bounded response cache and graceful shutdown. `campfire backup` writes an atomic SQLite snapshot;
the image provides ONCE's `/hooks/pre-backup` and `/hooks/post-restore` hooks.
The internal listener's conn loop is the owned loop in `internal/fastserve` by default (simdhttp head
parsing, one vectored write per response flush; upgrade and h2c connections are handed to an internal
net/http server). `CAMPFIRE_SERVER_LOOP` (default `on`; accepts `on`/`true`/`1` and `off`/`false`/`0`)
rolls that listener back to net/http with `off`. The public listeners never use the owned loop. Its
deliberate HTTP differences from net/http and its verification limits are recorded in
`plans/engine-41.md`; the ENGINE-53 measurement that made it the default is in the same file.

## Validation

```sh
bin/check                              # gofmt, assets, vet, all tests with the race detector
bin/check-assets                       # asset digests/importmap
bin/check-upgrade --rust-root ../once-campfire-rust
node bin/check-browser.mjs
bin/check-screens --out .cache/screens --only '**'
bin/check-acme
bin/check-container --image once-campfire-go:verification
```

The browser/upgrade tools use the Rust checkout's parity seed and installed Playwright. Screen
comparison runs both binaries on disposable copies of each seed, recording their hashes and keeping
the original parity masks. Reports distinguish screenshots, accessibility, server/live DOM, network
responses and Cable traffic. There are no Go-specific allowlists hiding failures.

Package tests use temporary databases and do not silently skip integration tests for a missing seed.
They include Rails signing/encryption and serialization vectors, 658 rich-text cases, 385 user-agent
cases, QR codes, route recognition, storage/ranges, transactions, access control, Cable, jobs,
90 Open Graph cases, 19 webhook cases and Web Push encryption/local delivery. Media metadata tests
run locally; byte-for-byte output tests require the pinned container toolchain:

```sh
docker build --target toolchain -t once-campfire-go:toolchain .
mkdir -p .cache/tmp .cache/docker-go-build .cache/docker-go-mod
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/src" -w /src \
  -e GOCACHE=/src/.cache/docker-go-build -e GOMODCACHE=/src/.cache/docker-go-mod \
  -e TMPDIR=/src/.cache/tmp once-campfire-go:toolchain \
  go test -tags 'sqlite_fts5 media_vectors' ./internal/storage
```

`bin/build` and `bin/check` use mise when Go is absent from PATH and keep temporary build files in
`.cache/`. Live ACME is tested against a local Pebble CA, including restart with the CA offline.
Container verification exercises setup, a live SQLite backup, offline restore, and restart.

## Benchmarks

Measured with 16 concurrent clients on an AMD Ryzen AI MAX+ 395,
with four hardware threads allocated to each app.

| HTTP workload (requests/sec) | Rails | [Django](https://github.com/basecamp/once-campfire-django) | [Laravel](https://github.com/basecamp/once-campfire-laravel) | [Express](https://github.com/basecamp/once-campfire-express) | [Elixir](https://github.com/basecamp/once-campfire-elixir) | [Go](https://github.com/basecamp/once-campfire-go) | [Rust](https://github.com/basecamp/once-campfire-rust) |
|---|---:|---:|---:|---:|---:|---:|---:|
| Room page | 241 | 170 | 164 | 559 | 722 | 96,568 | 36,260 |
| Messages page | 413 | 196 | 175 | 777 | 1,053 | 102,973 | 40,872 |
| Sidebar | 552 | 615 | 715 | 4,125 | 1,275 | 60,081 | 34,672 |
| Search | 435 | 315 | 305 | 1,294 | 1,156 | 132,259 | 33,299 |
| Post a message | 273 | 154 | 137 | 256 | 801 | 8,502 | 6,896 |

The Go column reflects this branch's engine, re-measured on this machine with
the official comparison harness; the other columns are the published cross-port
figures. The strict same-machine Rails/Rust/Go comparison is in
[Engine fork results](#engine-fork-results-this-machine) below.

See [`bench/`](bench/) for benchmark tooling and earlier measurements.


## Engine fork results (this machine)

This fork (`engine` branch) adds version-keyed in-memory caches, cached
pre-compressed response pieces, direct SQLite reads and writes through the
bundled C library, an adaptive group-commit write lane with checkpoints off the
request path, a compiled message renderer and route matcher, and an owned
HTTP/1.1 loop with vectored writes (all behind `CAMPFIRE_*` flags).

Measured with the official Elixir comparison harness running production Docker
images for Rails, Rust and this fork on the same machine, same fresh seed,
identical CPU pinning, 3 rotating repetitions, c=16 medians, zero HTTP errors:

| Route (req/s at c=16) | Rails | This fork | Rust | vs Rust | CPU/req vs Rust |
|---|---:|---:|---:|---:|---:|
| Room page | 222 | 96,568 | 36,771 | 2.63× | 38.3 vs 104 µs |
| Search | 382 | 132,259 | 33,951 | 3.90× | 27.7 vs 97.5 µs |
| Messages page | 406 | 102,973 | 42,059 | 2.45× | 35.5 vs 89.7 µs |
| Sidebar | 539 | 60,081 | 35,269 | 1.70× | 62.3 vs 108 µs |
| Post message | 269 | 8,502 | 6,772 | 1.26× | 296 vs 378 µs |
| /up | 4,176 | 453,789 | 242,009 | 1.88× | 5.8 vs 15.2 µs |
| Static CSS | 133,801 | 468,683 | 444,944 | 1.05× | 5.97 vs 7.47 µs |
| Avatar | 98,680 | 390,494 | 386,903 | 1.01× | 7.07 vs 7.96 µs |

Cable fan-out (messages delivered to every client, same harness and production
images): 100 clients 5,196 vs 4,043 (1.29×), 500 clients parity (1,101 vs
1,123), 1,000 clients parity (538 vs 542); saturated post→all p50 at 1,000
clients is faster than Rust (13.5 vs 16.1 ms). Earlier recording (ENGINE-60/61
paired medians): 100 clients 5,084 vs 4,261 (1.19×), 500 parity, 1,000 534 vs
548 (0.98×). See plans/wrong.md for the residual-gap analysis.

Correctness: `bin/check` (gofmt, assets, vet, full `-race` suite), browser
workflows, 178/178 applicable screen pixels and accessibility comparisons,
Rails/Rust upgrade interoperability, and native write/FTS verification.
Raw reports: `../once-campfire-elixir/bench/results/FINAL4-official-20261007/`
and `bench/results/final4-native-20261007/`.

## Known differences

- Templates use `html/template`. Whitespace, attribute serialization, some canonical form-action
  URLs, and response headers/validators differ from Rust. Strict server/live DOM and network layers
  therefore still fail in many inventory cells, even when screenshots, accessibility and workflows
  match. These failures remain visible in the validation report. Exact protocol parity for malformed
  parameters and every content-negotiation edge case is not claimed.
- WebSockets share serialized and compressed broadcast payloads through a small extension to
  coder/websocket v1.8.15 (see `third_party/websocket/README.campfire`). Outgoing queues hold 256
  frames; slow clients are disconnected. Authorization is checked afresh for each publication,
  batching distinct sessions per room. Rust uses different stream queues.
- Go ignores typing commands for rooms that have been deleted; Rust can still echo them to an
  already subscribed socket. The composer shows the same deleted-room message.
- The response cache uses least-recently-used eviction instead of Rust's sampled eviction. The Go
  message-fragment cache is also independently implemented. It retains versioned message lists
  and sidebar HTML; sidebar hits are served from the version-keyed fragment cache with no row
  reads and no page setup (the sidebar version registry is seeded from the database on first use
  and bumped by every sidebar-visible write; see the `CAMPFIRE_FRAGMENT_CACHE_MB` notes above),
  while room membership and permission data are still read afresh for non-sidebar pages.
  Room pages also cache their surrounding HTML keyed by fresh page data, inserting the current
  messages and refresh timestamp on every request. Responses assemble cached message bytes with fresh page HTML and derive validators from part
  lengths and hashes, so ETag values differ from both the original Go implementation and Rust.
- The front answers `/up` from a fixed-response table (`CAMPFIRE_FRONT_FIXED`, default `on`;
  same value shapes and warning policy as the other switches): the first unconditional GET
  captures the health response once per content encoding (identity and gzip) and later
  requests replay the captured bytes without touching the application. Replays are byte-
  identical to the application path (status, headers, validators, Vary and the captured
  X-Cache value), the table is keyed by host, path and the exact Accept header value (the
  application negotiates the health format from Accept), and conditional, Range, other-host
  or non-negotiable requests still reach the application. The replay lane of the ordinary
  response cache (compression wrapper fast path, single-allocation header copy) and the
  per-header-value encoding negotiation memo are wire-identical by construction. `forward`
  edits the request headers in place instead of cloning the request; net/http allocates a
  fresh `Request` and header map per request, so the clone was pure overhead.
- The default version label and fallback VAPID subject identify `once-campfire-go`. Explicit version,
  VAPID keys and subject settings remain supported.
- Native host media output can differ with installed library versions. All byte-golden media tests
  pass with the pinned container libraries. Web Push is verified locally, not against external push
  providers.

As in Rust, background queues are bounded and in-process: graceful shutdown drains work, but a
process crash can lose queued jobs. See [the implementation record](plans/go-conversion.md) for
coverage and validation scope.

## License

MIT; see [MIT-LICENSE](MIT-LICENSE). Third-party notices for copied/adapted algorithms are in
[licenses](licenses/) and [internal/html/LICENSE](internal/html/LICENSE); module dependencies retain
their own licenses.
