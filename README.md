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
override individual locations; `RAILS_ENV` defaults to `production`. `CAMPFIRE_ENGINE` controls route
ownership for the performance engine: `on` (default) serves owned routes in the engine with the legacy
handler as fallback, `off` disables ownership for A/B and rollback, and `force` is reserved for tests.
`CAMPFIRE_FRAGMENT_CACHE_MB` sizes the legacy in-process HTML fragment cache (default 32; `0`
disables caching), which holds per-message fragments, the sidebar and the legacy recorded shells
when the piece path is off; the engine piece cache reads the same key when routes migrate to it, so
with both full the worst case is 2× the configured MiB (64 MiB by default), kept intentionally
during the strangler migration. `CAMPFIRE_CABLE_FAST` (default `on`) gates the Action Cable
fan-out fast paths (ENGINE-40): per-wake batched/vectored writes and the exact-payload frame
cache; `off` selects the legacy one-write-per-frame path for A/B. Sidebar fragments are keyed by a version, not by their content: an
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
message ids and updated-at stamps and stored as raw bytes plus a complete gzip member. Setting it
`off` restores the legacy per-request render, compression and ETag path for A/B and rollback.
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
`0` disables storage while still serving from freshly rendered pieces). Each cached piece charges
its raw bytes, its gzip member and the key, so one piece costs up to about twice its HTML size;
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
| Room page | 241 | 170 | 164 | 559 | 722 | 3,860 | 36,260 |
| Messages page | 413 | 196 | 175 | 777 | 1,053 | 5,573 | 40,872 |
| Sidebar | 552 | 615 | 715 | 4,125 | 1,275 | 19,753 | 34,672 |
| Search | 435 | 315 | 305 | 1,294 | 1,156 | 7,053 | 33,299 |
| Post a message | 273 | 154 | 137 | 256 | 801 | 4,767 | 6,896 |

See [`bench/`](bench/) for benchmark tooling and earlier measurements.

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
