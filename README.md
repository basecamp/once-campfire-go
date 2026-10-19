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
Compiled production route recognition is controlled separately by
`CAMPFIRE_COMPILED_ROUTES` (default `on`; `off` restores the regex matcher).
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
authorized/delivered without the lock. Publication authorization also carries the
pinned reader's `PRAGMA data_version` generation, so foreign commits invalidate
the memo even without a local counter bump. The idle heartbeat's local-counter
gate is a separate mechanism. The write-helper audit is pinned in
`internal/database/versions_test.go`.
Logout goes through `DB.DeleteSession` so the session deletion bumps the generation before
the caller touches the hub. Sidebar fragments are keyed by a version, not by their content: an
in-process registry (seeded once from the database on first use; bumped after every sidebar-visible
write — room create/rename/delete, membership join/leave/removal, unread changes via message
create and presence, user rename/avatar/role, direct-placeholder transitions, account room
restrictions) yields the key without reading any room, membership or placeholder rows, so a cache
hit serves the fragment with no row reads and no page setup, and the entry accounting is the same
bounded LRU as every other fragment. The registry is per process, but HTTP cache keys also
carry the request's observed generation from pinned `PRAGMA data_version`: commits from
another connection invalidate sidebar, search, message pieces, reference windows and read caches. The
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
several pieces. Recorded pages negotiate gzip or identity before preparing payloads or shells,
even with the flag on; rejecting both returns 406 without piece preparation. They do not
precompute unreachable zstd frames. The multi-frame zstd assembly remains in
`internal/piececache` for non-browser clients and the corpus that exercises it.

Raw-wire tests mask only Date. `WriteRecorded` intentionally replays one byte-slice write,
not the original write history: a 2049-byte `WriteString` emits chunks `800` + `1`, and
1000+2000 split writes emit `800` + `3b8`, while replay emits `801` and `bb8` respectively
(hexadecimal chunk lengths). Headers and decoded bodies match. The replay bytes are checked
against a single-write net/http oracle; fresh split-write bytes against a split-write oracle.
Page coverage includes HEAD/304, identity/gzip and zstd negotiation, cold/warm caches,
alternate Host/forwarded origin, and bodies below/above the request-drain budget. This is
Go loop/map/replay parity coverage, not a new Rails or browser parity claim.
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
normalized query, corpus version, membership version, observed database generation), skipping the FTS
scan and the `RecentSearches` read on a hit. The unused `Rooms` read was removed from both paths;
the message fragments come from the recorded piece cache (same
content-keyed pieces a miss would produce), so no fragment re-renders. The corpus version is a
global counter bumped by every committed message create/edit/delete, boost change and room destroy;
the membership version is bumped by every committed membership grant/revocation, involvement change
and account create/deactivate. A hit therefore serves the byte-identical page a re-read would
produce within the observed generation. External writes and DDL, including dropping the FTS index,
invalidate old entries; a post-DDL miss must query the database and can fail if the index is absent.
The isolated hit-path test explicitly seeds an entry under the post-DDL generation. POST
`/searches` and `DELETE /searches/clear` keep their database behavior and also purge the user's
cached entries, so the rendered recent-searches list stays current. Deliberate trade-offs: on a hit
the page's "last room" link is still read per request (it depends on the `last_room` cookie), and a
generation capture is not a transaction-wide snapshot. A rejected capture bypasses dependent caches
without relabeling earlier reads. Keys use SHA-256 digest identity rather than full-key verification;
long queries are hashed incrementally in fixed scratch without query-sized allocation.
`CAMPFIRE_SEARCH_CACHE_MB` sizes it (default 16; `0` disables storage; an entry larger than a
quarter of the budget is not cached).
`CAMPFIRE_WRITE_QUEUE` (default `on`; also accepts `true`/`1` and `off`/`false`/`0`, warning on
anything else) routes message creation through a single writer goroutine that commits everything
queued at a drain in one `BEGIN IMMEDIATE` transaction (group commit), each job in its own savepoint
so a failing job rolls back exactly its own statements while the batch commits. Message, body,
attachments, FTS and unread state commit together on every lane. The port-owned
`campfire_push_outbox` table and insert trigger record push intent in that same transaction.
This deliberately strengthens the reference's asynchronous callback boundary. The writer
connection disables SQLite's
auto-checkpoint (`PRAGMA wal_autocheckpoint=0`) and a separate connection runs
`PRAGMA wal_checkpoint(PASSIVE)` every `CAMPFIRE_CHECKPOINT_MS` milliseconds (default 1000).
Scheduled checkpoint fsyncs run off the writer; threshold PASSIVE/RESTART runs before
the next batch is accepted and can delay queued writers. Durability does not move: the
database stays WAL mode with `synchronous=NORMAL` and the default `journal_size_limit` (unset,
-1), commits are never fsynced, and what committed since the last checkpoint can be lost to a power
failure (never to a process crash); every checkpoint syncs the WAL, as the auto-checkpoint did.
Setting `off` restores per-request transactions and the writer's auto-checkpoint. The response
checksum is covered by write-lane parity tests. Sidebar and search-cache version counters move
after commit on both paths, so the write lane never serves a version-keyed
cache that hides a committed message or its index row.
Push workers discover outstanding intent at startup and every second; queue saturation loses only
a wake. Each scan reads up to 128 messages and records at most 128 recipient outcomes
per acknowledgement transaction, with a five-second network/query budget. Network calls
run outside that transaction. `campfire_push_progress` records successful subscriptions
and attempt order: acknowledged recipients are excluded, and untouched recipients precede
failed ones. Expiry stops new deliveries; untouched messages stay due without a retry update.
Each failed recipient backs off at least 30 seconds from its persisted attempt time
(rounded up to whole seconds). Sleeping failures are never retried to fill a batch;
the earliest eligible failure determines the next message retry once untouched work
is exhausted. A partial fan-out remains due but moves behind older scheduled messages,
so even a failure cohort larger than one batch or delivery budget yields to later messages.
Eligibility and subscription keys are refreshed before each send with a fixed query
and one parsed mention set per message. The progress/acknowledgement transaction adds a commit and
can invalidate generation-keyed caches, but does so once per bounded scan, not per message.
Retention policy: persistent failures remain indefinitely (no automatic dead-letter deletion)
until delivery succeeds, eligibility disappears, the message is deleted, or VAPID is disabled.
Operators must monitor pending intent growth; there is no finite backlog-size guarantee.
Delivery is **at-least-once**, not exactly-once: a crash after remote acceptance but before
the scan's progress commit can duplicate that scan's accepted notifications. Committed
recipient acknowledgements are not replayed. Replay uses current message content, membership
and subscriptions; deleting a message cascades intent and progress. Disabled VAPID intentionally
discards pending intent. Websocket publication is best effort and is not replayed. Subprocess
crash tests cover primary-commit, pre-notification, remote acceptance before progress commit,
and interruption after the second INSERT of a three-recipient progress transaction;
simulated push-service and DNS
failures, startup replay and queue saturation are covered locally, not against a live push provider.

Cable subscriptions reauthorize sessions before side effects, and presence checks the token and
active user in the write transaction. Heartbeats observe foreign database commits. Frame-cache
admission now resolves compression and charges both backing capacities, keys and bookkeeping;
this eagerly compresses cache misses even without compressed recipients. Sidebar gates are
direct-mapped into 4096 slots (collisions cause misses), with fixed-size digest keys. No throughput
or allocation benchmark was run for this durability/memory fix set.
Deterministic independent-connection revocation tests cover search and sidebar requests paused
after generation capture, with whole-response caching enabled and disabled. These are local
correctness checks; no throughput measurements were run for the data-plane fixes.
`CAMPFIRE_CHECKPOINT_PAGES` (default 10000; `0` disables it; a negative or unparseable value warns
and keeps the default) is the WAL-growth threshold: the write lane reads the WAL's frame count
after each drain and, once the WAL reaches that many frames, runs `PRAGMA wal_checkpoint(PASSIVE)`
then `RESTART` on the writer connection between jobs. PASSIVE backfills every frame no reader is
pinning and RESTART waits for the readers, so the next commit resets the WAL to its first frame.
This is the Rust port's `WAL_LIMIT_PAGES` plus its writer-side `restart_wal`, without its per-message
queue hop; it exists because with only the timed PASSIVE checkpoints, a busy post workload grows the
WAL file without bound — measured on the reference seed at 4.5–5.2 GB in ten seconds at 8.4 K
posts/s, because active readers starve SQLite's automatic WAL restart. With the threshold on, that
same post workload held the WAL at ~40 MB on btrfs at c=16
(`TestWALStaysBoundedUnderSustainedWrites` in `internal/database` asserts the bound in process), at
no measured throughput cost — a result for that workload and filesystem, not a general figure.
The threshold is a threshold, not a hard bound. The check runs after a drain, so a batch (or a run
of drains) can overshoot it by what it wrote between checks; a single large message can add many
frames at once; commits by other writers — sessions, accounts, uploads, another process — are
invisible to the write connection's WAL hook until it commits again (the database/sql lane's probe
is throttled to 5 ms, so its check can lag by that much of writes). There is no backpressure: the
writer never refuses or delays a write to stay under the threshold. Growth is bounded only while
restarts can complete: a RESTART cannot reset the WAL while a reader holds a WAL read slot, so with
continuing writes and a persistently pinned reader the WAL grows without bound until that reader
goes away — the backoff deliberately permits that rather than stalling the writer on every drain.
What the mechanism does fix is that a refused restart is not misreported and not retried blindly:
the RESTART's result row is read, so a checkpoint SQLite refused (a reader still pinning frames, or
another checkpoint holding the lock) is counted as an incomplete attempt rather than as a restart;
the attempt runs with a 250 ms busy-handler timeout (`write.CheckpointBusyMS`) instead of the
connection's 5000 ms, which bounds only SQLite's busy-handler retry — not the attempt's wall time,
which also covers connection acquisition, page copying, filesystem synchronization and scheduling;
and refused attempts are spaced by a backoff (25 ms doubling to 2 s). A checkpoint gate shared with
the background checkpointer keeps a scheduled PASSIVE and a writer restart from refusing each
other; the writer defers (and counts the deferral) rather than queueing. The writer's counters —
invocations, attempts, completed, incomplete, errors, deferred, and the stall split into the
backfill step and the RESTART step — are in the telemetry summary.
`CAMPFIRE_CHECKPOINT_MS=0` disables the timed fallback and leaves the growth threshold to schedule
checkpoints; with both `CAMPFIRE_CHECKPOINT_MS=0` and `CAMPFIRE_CHECKPOINT_PAGES=0` the fallback is
restored to 1000 ms (a warning says so), so a run always checkpoints. The final drain is unchanged:
`Close` drains the writer, runs one last PASSIVE checkpoint and lets the last connection remove the
WAL.
`CAMPFIRE_CHECKPOINT_TELEMETRY` (default `off`; same value shapes) logs one line per checkpoint —
WAL frames, frames moved, backlog, busy, duration, time since the last checkpoint and the `-wal`
file size — and a summary at shutdown, plus one line per WAL restart attempt; the counters behind
it are always kept and an in-process snapshot is available to tests. It changes no behaviour and is
meant for workload runs. Its cost, with logging off as well as on, is one `time.Now`, a few atomic
stores and one `os.Stat` of the `-wal` file per checkpoint (once a second at the default interval),
on the checkpointer's own goroutine. The write lane's frame check is not free either: it runs once
per drain, takes the lane connection's mutex and makes one cgo call to read the hook's counter (and
on the database/sql lane, at most one `PRAGMA wal_checkpoint(NOOP)` statement per 5 ms). Neither
runs on the commit path or per message.
`CAMPFIRE_FASTDB_WRITE` (default `on`; accepts the same value shapes and warning policy as
`CAMPFIRE_WRITE_QUEUE`) runs the message create transaction as direct prepared statements on one
serialized fastdb connection (`internal/fastdb/write` over `internal/fastdb/csqlite`) instead of
`database/sql`: the same six statements — the authorized creator lookup (membership check and name
in one statement), message insert, room touch, rich-text body, attachment, and search-index insert
in the primary transaction together with unread state and push intent — bound positionally (int64/text/null), prepared once
and reused across jobs, with the same `BEGIN IMMEDIATE`, savepoint-per-job, single-commit-per-batch
and atomic group-commit shapes the `database/sql` lane has. The pragma set is identical
(`busy_timeout=5000`, `foreign_keys=on`, WAL, `synchronous=NORMAL`, `cache_size=2000`,
`wal_autocheckpoint=0` with the off-writer PASSIVE checkpointer unchanged), and the error
mapping matches (`errors.Is` against the same sentinels: `sql.ErrNoRows`, the csqlite
constraint/busy codes; only the error text formatting differs). The statement-count tests pin
the per-post text/order/count on both lanes: 11 counted statements with the flag off, 5 with it
on (the create transaction's six statements leave the counted driver), and the direct lane's
recorded stream is pinned by text and order in `internal/database/fastwrite_test.go`. The
twin-database differential test drives identical create workloads (plain, attachment, staged
upload, webhook, queued, and the forbidden/no-creator/FK failure cases) through both lanes and
compares every touched row byte-for-byte. The flag only affects the queued lane: with
`CAMPFIRE_WRITE_QUEUE=off` the per-request transaction path stays `database/sql`, exactly as
before. Each create-transaction statement that used to cross the cgo boundary once per bind plus
once each for the step, the last-insert-id read and the reset now crosses once: `csqlite.Stmt.ExecTyped`
runs the typed binds, the step, the optional rowid capture and the reset inside C
(`fastdb_write_run`), and `write.WriteStmt`'s `ExecInsertMessage`, `ExecTouchRoom`,
`ExecInsertRichText`, `ExecInsertAttachment`, `ExecInsertSearchIndex` and `ExecBumpUnread` are
those fused shapes. The message INSERT drops from eight cgo transitions to one and the rich-text
INSERT from six to one, pinned by `runtime.NumCgoCall` on a bare write connection in
`internal/database/cgo_budget_test.go` (the unfused sequence is measured live in the same test,
not asserted from history) with `testing.AllocsPerRun` at zero. Binding stays `SQLITE_TRANSIENT`
and no SQLite buffer is read after the reset, so the ownership argument is unchanged; the SQL
texts, bind order, error mapping and reset-on-every-path reuse are the unfused ones, held by the
lane differential tests and the `internal/fastdb/write` fused/unfused row comparison.
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
the reference list (message id, room, update stamp) — is cached keyed by
`(observed generation, room, room updated_at, anchor, direction)` and served warm at one
in-memory lookup with no scan. The former timestamp-only key measured 48 ns/op,
0 allocs, versus 20.8 µs/op for scanning; that is a historical measurement.
External inserts/deletes miss even when the room timestamp is preserved. References
do not validate rendered bytes: pagination computes its ETag after rendering.
A hit returns only references; full message rows (rich
text, author, boosts) hydrate exactly where they always did, on a per-message fragment cache miss,
which also keeps the around/after pages on the same hydration path the before pages always used.
The cache is bounded by bytes and by 4096 entries, pruned oldest-first to 75% of the budget like
the fragment cache; a single window never exceeds a quarter of the budget.
`CAMPFIRE_RESPONSE_CACHE_MB` sizes the whole-response cache (default 64, the published topology of the
C reference's response-body cache; `0` disables storage and every lookup misses, leaving the
finer-grained caches below in charge — byte-identical). It stores the completed HTML responses of the
room, messages-page and search routes — the exact wire bytes a fresh render produced (the assembled gzip
member for gzip clients, the joined pieces for identity clients) with their entity headers. A
warm request re-checks the session and the observed database generation, then serves the stored body:
no message reads, no template execution, no per-request compression or assembly. The key covers the
generation (`PRAGMA data_version` on the pinned reader), the authenticated user, the request URI and
form, the identity headers (cookie, Accept, Turbo-Frame, user agent, origin), the negotiated encoding
and the process revision, so a hit is byte-identical to a fresh render by construction and a commit —
local or external — moves the generation and misses. A session flash renders fresh and never stores.
Search participates in this round, with its result cache serving response-cache misses.
Sidebar retains its own generation-gated whole-page cache.
Entries are LRU, pruned to fit the budget, and a single entry is capped at an eighth of it. The
generation gate observes local and external commits through the pinned `PRAGMA data_version`
reader. Read and reference caches retain the pre-read generation through admission;
an unavailable observation bypasses them. This is not a transaction snapshot across all page reads.
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
value logs a warning and keeps the runtime setting. The shipped image sets `CAMPFIRE_GOGC=200`
(ENGINE-47): on the harness read workload, with aligned-window per-request accounting, it halves
GC CPU per request (median 1.65 → 0.84 µs) and cuts total CPU per request by 4.9% (23.84 →
22.66 µs; pooled paired ratio 0.957 over 64 pairs, against an identically configured noise-floor
arm at 1.001), at the cost of a higher peak RSS (median 74 → 87 MiB). `CAMPFIRE_GOGC=100` rolls
the default back. No `CAMPFIRE_GOMEMLIMIT` is shipped: a 256 MiB limit was measured as
non-binding and indistinguishable from no limit at that peak. `bench/gc-policy` reproduces the
measurement; `bench/results/gc-policy-opt-runtime/README.md` records it, the warmup check that
validates the accounting (`bench/results/gc-policy-warmup-check/`), and the separate PGO on/off
measurement (`bench/results/pgo-on-off/`), where no PGO benefit was detectable on this workload.

The build is profile-guided: `cmd/campfire/default.pgo`, collected from the production workload
by `bin/pgo-profile` (the five harness routes through the public front listener), is the PGO
input for `bin/build` and the image build via `-pgo=auto`; the build falls back to no PGO when
the file is absent. `cmd/campfire/default.pgo.provenance.json` identifies the binary that was
profiled from its own `go version -m` build info (revision, build settings), records the
effective runtime settings from the server's startup log, and pins the checkout's build inputs by
content with an explicit statement of whether they can stand for that binary. It does not claim
the profile the binary was built with: `go version -m` names the `-pgo` path and never the
profile's content, so the file at that path is reported as an unproven candidate, captured before
a run can overwrite it. **No PGO benefit was measured**: on/off is compared in
`bench/results/pgo-on-off/` and no difference was detected at that sample size on this host, and
the committed profile is a bootstrap input (collected from a `-pgo=off` build of the same
revision). The profile ships as the lever's mechanism, not as a measured speedup.

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
The owned loop observes disconnects after request-body completion and cancels the active request
context. Its one-byte background reader preserves pipelining and is interrupted/joined before
response finalization. As in net/http, a successfully observed pipeline byte stops observation
until that byte is consumed; unread bodies are not read concurrently with the handler.
The wire fixes and reader lifecycle are recorded in `plans/fastserve-wire-fixes.md`.
The recorded-response path sets the negotiated `Content-Encoding` in the header map before emission
even on the precomposed-framed path (where the map never reaches the wire), so the front middleware
passes a pre-encoded body through instead of starting its own gzip member after it — a lone gzip
header emitted at close had tripped the owned loop's Content-Length guard and ended the keep-alive
connection after every framed gzip response.

## Historical validation

These browser/container/interoperability runs describe the revisions recorded in
`plans/validation.md`; final coherency-fix checks are recorded separately there.

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

Measured with 16 concurrent clients on an AMD Ryzen AI MAX+ 395 with 32 GB RAM,
with four hardware cores allocated to each app.

| HTTP workload (requests/sec) | Rails | [Django](https://github.com/basecamp/once-campfire-django) | [Laravel](https://github.com/basecamp/once-campfire-laravel) | [Express](https://github.com/basecamp/once-campfire-express) | [Elixir](https://github.com/basecamp/once-campfire-elixir) | [Go](https://github.com/basecamp/once-campfire-go) | [Rust](https://github.com/basecamp/once-campfire-rust) | [C](https://github.com/basecamp/once-campfire-c) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Room page | 4,101 | 1,507 | 3,872 | 42,636 | 5,350 | 102,916 | 106,494 | 137,524 |
| Messages page | 4,115 | 1,596 | 3,995 | 74,362 | 5,712 | 110,629 | 102,697 | 144,642 |
| Sidebar | 4,333 | 1,873 | 4,493 | 94,329 | 5,949 | 132,733 | 120,294 | 152,002 |
| Search | 4,282 | 1,862 | 4,172 | 84,665 | 5,848 | 113,333 | 121,378 | 149,487 |
| Post a message | 330 | 262 | 794 | 2,155 | 1,278 | 8,250 | 8,037 | 7,530 |
| Backend KLOC | 5.5 | 5.3 | 4.1 | 6.8 | 11.6 | 23.2* | 30.7 | 94.8 |

The historical Go column's measured revision was not recorded (these values
entered the README in merge `60bfeff`); it is not a current-tree measurement.
The numbers were measured on this machine with
the official comparison harness; the other columns are the published cross-port
figures. The strict same-machine Rails/Rust/Go comparison is in
[Engine fork results](#engine-fork-results-this-machine) below. The Go KLOC is
the upstream port's published count; this fork's engine backend is additional
and not counted here.

KLOC counts backend code plus executable code in templates; excludes plain
HTML, frontend, tests, dependencies and generated files.

[Shared verification](https://github.com/basecamp/once-campfire-verification) · [Detailed results](https://github.com/basecamp/once-campfire-verification/blob/main/docs/performance-review.md).

See [`bench/`](bench/) for benchmark tooling and earlier measurements. The
runtime levers are measured separately: `bench/gc-policy` runs `CAMPFIRE_GOGC`/
`CAMPFIRE_GOMEMLIMIT` arms — and, with `--binary NAME=PATH`, PGO builds against
each other — with aligned-window per-request accounting and an identically
configured noise-floor arm (`bench/results/gc-policy-opt-runtime/`,
`gc-policy-warmup-check/`, `pgo-on-off/`), and `bin/pgo-profile` collects the
build's PGO input (`cmd/campfire/default.pgo`).


## Engine fork results (this machine)

This fork (`engine` branch) adds version-keyed in-memory caches, cached
pre-compressed response pieces, direct SQLite reads and writes through the
bundled C library, an adaptive group-commit write lane with checkpoints off the
request path, a compiled message renderer and route matcher, and an owned
HTTP/1.1 loop with vectored writes (all behind `CAMPFIRE_*` flags).

Measured with the official Elixir comparison harness running production Docker
images for Rails, Rust and this fork on the same machine, same fresh seed,
identical CPU pinning, 3 rotating repetitions, c=16 medians, zero HTTP errors:

| Route (req/s at c=16) | Rails | Go `3305f44` | Rust | vs Rust | CPU/req vs Rust |
|---|---:|---:|---:|---:|---:|
| Room page | 222 | 96,568 | 36,771 | 2.63× | 38.3 vs 104 µs |
| Search | 382 | 132,259 | 33,951 | 3.90× | 27.7 vs 97.5 µs |
| Messages page | 406 | 102,973 | 42,059 | 2.45× | 35.5 vs 89.7 µs |
| Sidebar | 539 | 60,081 | 35,269 | 1.70× | 62.3 vs 108 µs |
| Post message | 269 | 8,502 | 6,772 | 1.26× | 296 vs 378 µs |
| /up | 4,176 | 453,789 | 242,009 | 1.88× | 5.8 vs 15.2 µs |
| Static CSS | 133,801 | 468,683 | 444,944 | 1.05× | 5.97 vs 7.47 µs |
| Avatar | 98,680 | 390,494 | 386,903 | 1.01× | 7.07 vs 7.96 µs |

Cable fan-out at Go `3305f44` (messages delivered to every client, same harness and production
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

The public front cache bypasses dynamic user media, account logos and Active
Storage routes. Avatar requests retain application session authentication;
signed disk requests recheck capability expiry and file existence on every
request. Session revocation, capability expiry and media deletion therefore
cannot be bypassed by the server's shared response cache. Existing browser
cache lifetimes in the media response headers remain reference-compatible.

Browser writes use Rust’s token-free Fetch Metadata policy: exact `same-origin` or
`same-site`, a matching Origin when provided, and absent metadata only on plain
HTTP with TLS disabled. Signed disk uploads retain their authenticated capability
exemption; bot-key endpoints exempt authenticated bots only.


- Session-transfer auto-submit forms explicitly close their form tag; the pinned Rails
  reference omitted it.
- Background sidebar refreshes preserve an open New Ping form and selected recipients.

- Sidebar connection refresh waits for the current Turbo frame to finish loading,
  preventing an aborted response on startup or reconnect. Obsolete connections and removed frames do not reload.

- Search selects the newest 100 matching messages by insertion ID, then displays them in ID order. Backdated messages can appear in a different order from the original Rails app.

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
  Search layouts similarly surround a captured immutable result body; misses rerun the full
  query. Complete GET gzip representations have a bounded memo keyed by immutable part
  identities, not by wire ETags. Authorization, sessions and request observations stay fresh.
  Room pages also cache their surrounding HTML keyed by fresh page data, inserting the current
  messages and refresh timestamp on every request. Responses assemble cached message bytes with fresh page HTML and derive validators from part
  lengths and hashes, so ETag values differ from both the original Go implementation and Rust.
- Message fragments, legacy lists and recorded-list pieces carry the observed database generation
  and request host/origin in their cache keys (recorded identities length-prefix both strings).
  A rejected ingress generation disables dependent read, reference, fragment and piece caches,
  including when whole-response caching is off. A dedicated pinned SQLite reader observes local and foreign
  commits (`PRAGMA data_version`), so external edits without timestamp changes and
  host-dependent rich-text filtering are reflected in the caches without manual invalidation.
- Message pagination uses its complete rendered body for its ETag, including related-user and
  boost changes. It omits Last-Modified because message timestamps cannot describe external
  presentation edits.
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
- Storage keys containing separators, NUL, or parent-directory shards are rejected before
  filesystem access. Valid keys retain the existing storage layout.
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

Shared verification harness (`basecamp/once-campfire-verification`, revision
`ec02deb`, same machine, tmpfs runtime, c=16, 3 rounds, zero errors) against
Rust revision `f9dca47`, measuring the final train `1b143bf` (image
`once-campfire-go:final2`; the branch tip differs only in documentation):

| Route | Rust `f9dca47` | Go `1b143bf` | vs Rust |
|---|---:|---:|---:|
| Room page | 109,468 | 148,605 | 1.36× |
| Messages page | 104,716 | 194,387 | 1.86× |
| Sidebar | 124,164 | 162,273 | 1.31× |
| Search | 126,215 | 201,560 | 1.60× |
| Post message | 8,177 | 15,392 | 1.88× |

Every route is ahead of Rust (1.31×-1.88×); the local Rust image reproduces the
published Rust figures within ~2-4%, and the harness verified 660,976
acknowledged writes with zero errors or invalid responses. Against the C port at
native tmpfs conditions the final train leads on messages (1.14×), search
(1.21×) and post (2.29×), with room and sidebar near parity (0.94×/0.97×).

The earlier matched run on `34782d0` (before the composition, durability and
conditional fixes) measured 149,403 / 192,759 / 166,116 / 197,400 / 15,817 —
the same shape, within the run-to-run spread. A `GOSIMD=scalar` arm on the same
image (SIMD kernels disabled at runtime) measured 0.989-0.994 of the SIMD run
on every route: the SIMD request parser contributes about one percent
end-to-end.
