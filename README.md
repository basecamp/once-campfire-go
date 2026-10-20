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
See the [known differences](#known-differences) before replacing an installation.
The [historical port validation](plans/validation.md) is dated evidence, not proof of current
correctness or performance.
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
override individual locations; `RAILS_ENV` defaults to `production`. Backup and restore use these
same settings, including custom roots and database filenames.
`campfire db:prepare` initializes an empty database and checks migration versions;
existing databases missing migrations are rejected.

The public listener uses `HTTP_PORT=80`. Set `TLS_DOMAIN` for automatic ACME certificates and HTTPS
on `HTTPS_PORT=443`. The internal application listener defaults to `TARGET_BIND=127.0.0.1` and
`TARGET_PORT=3000`. The front server provides HTTP/2, optional H2C, gzip/zstd with compression jitter,
a bounded response cache and graceful shutdown. `campfire backup` writes an atomic SQLite snapshot;
the image provides ONCE's `/hooks/pre-backup` and `/hooks/post-restore` hooks.
`campfire restore` restores `backups/<database filename>` from the configured storage root to the
configured database path. Stop the application first; restore is an offline operation. A missing
snapshot is a no-op. Empty snapshots or snapshots without migration metadata are rejected without
replacing the database.

## Validation

```sh
bin/check                              # gofmt, assets, vet, all tests with the race detector
bin/check-assets                       # asset digests/importmap
bin/check-upgrade --rust-root ../once-campfire-rust
bin/check-parity --seed /path/to/parity-seed --go-binary ./campfire \
  --rust-binary /path/to/rust/campfire --out .cache/contracts
node bin/check-browser.mjs
bin/check-screens --out .cache/screens --only '**'
bin/check-acme
bin/check-container --image once-campfire-go:verification
```

`bin/check-parity` compares authentication, direct-ping reuse, pagination, sidebar documents,
message creation/editing/deletion, room conversion/membership revocation and partial updates,
account restrictions/custom styles, retained inactive direct-room participants, involvement pages
and nullable updates, direct uploads, signed blob/disk/representation downloads, multipart attachment
replacement/purge, avatar assignment/deletion and nested direct-upload metadata against pinned Rust. Supply a prepared parity seed and
binaries built for the same host. Each workflow uses a fresh SQLite backup, including WAL state, and a copy
of seeded storage. It checks HTTP behavior, controls, authorization, fresh reads and committed
message/FTS state, not byte-identical HTML or internal implementation details. Reports include
binary, tool and seed-snapshot hashes; failures remain visible and produce a nonzero exit status.
Current verification scope and retained evidence are summarized in the
[owned-operations validation record](bench/results/owned-operations-20261015/README.md).
Shared flows are functional checks, not complete parity.
The paired HTTP tool does not cover malformed Rack parameter compatibility, multipart metadata,
JavaScript, live Cable delivery or crash durability.

The browser command starts a fresh disposable Go instance and runs the
[shared Campfire browser flow](https://github.com/basecamp/once-campfire-verification), the same
flow used for Rails, Elixir and Rust. Clone that harness alongside this repository, run `npm ci`
and `npx playwright install chromium` there, then run `node bin/check-browser.mjs` here.
`VERIFICATION_ROOT` selects another harness checkout; `GO_BINARY` selects another Go binary.
Use `--turbo-cancellation` only for the focused regression when changing the port's Turbo override.
The upgrade and screen tools use the Rust checkout's parity seed and installed Playwright. Screen
comparison runs both binaries on disposable copies of each seed, recording their hashes and keeping
the original parity masks. Reports distinguish screenshots, accessibility, server/live DOM, network
responses and Cable traffic. There are no Go-specific allowlists hiding failures.

Package tests use temporary databases and do not silently skip integration tests for a missing seed.
They include Rails signing/encryption and serialization vectors, 658 rich-text cases, 385 user-agent
cases, QR codes, route recognition, storage/ranges, transactions, access control, Cable, jobs,
90 Open Graph cases, 19 webhook cases and Web Push encryption/local delivery. Media metadata tests
run locally. Changes to media processing also use the reference output checks, selected with the
`media_vectors` build tag. The checked-in video goldens require AMD64 FFmpeg 7.1.5; ARM64 FFmpeg with the same
version produces different JPEG bytes. Architecture is part of the toolchain identity:

```sh
docker build --platform linux/amd64 --target toolchain -t once-campfire-go:toolchain .
mkdir -p .cache/tmp .cache/docker-go-build .cache/docker-go-mod
docker run --rm --platform linux/amd64 --user "$(id -u):$(id -g)" -v "$PWD:/src" -w /src \
  -e GOCACHE=/src/.cache/docker-go-build -e GOMODCACHE=/src/.cache/docker-go-mod \
  -e TMPDIR=/src/.cache/tmp once-campfire-go:toolchain \
  go test -race -tags 'sqlite_fts5 media_vectors' ./internal/storage
```

`CAMPFIRE_STORAGE_VECTORS` can explicitly select a separate Rails capture for another architecture;
the checked-in vectors remain the default. Originals, derivatives, attributes and metadata are
checked without changing encoder flags or weakening byte comparisons. Historical
[media verification evidence](https://github.com/nijaru/once-campfire-go/blob/d287f419d052a196c52497acb2c06fca85742f27/bench/results/acceptance-corrections-20261007/README.md)
distinguishes unchanged-golden verification from the separate ARM64 Rails comparison.

`bin/build` and `bin/check` use mise when Go is absent from PATH and keep temporary build files in
`.cache/`. Live ACME is tested against a local Pebble CA, including restart with the CA offline.
Container verification exercises setup, a live SQLite backup, offline restore, and restart with
default paths, storage/environment overrides, database overrides, combined overrides, and URI
metacharacters in paths.

## Profiling

Once correctness checks pass, use representative CPU/allocation profiles to choose
optimization work. Investigate blocking, scheduler behavior and SQL when CPU does
not explain elapsed time. Prefer quiet native hardware when available; local/VM
profiles remain useful diagnostics, not interchangeable capacity measurements.
Validate changes with complete responses, persisted-write audits and matched
end-to-end checks. A smaller profile percentage alone is not an improvement.

The CLI supports an opt-in CPU profile. With the normal server environment pointed
at a disposable fixture, run:

```sh
mkdir -p .cache/profiles
GO_CPU_PROFILE=.cache/profiles/cpu.pprof ./campfire server
# Exercise the representative workload, then stop the server gracefully.
go tool pprof -top ./campfire .cache/profiles/cpu.pprof
```

The profile covers the process lifetime, including setup/authentication. Separate
that work from the application phase being investigated; use a bounded phase
capture if it would otherwise dominate. Graceful shutdown flushes the profile.
Instrumented runs guide diagnosis; keep final benchmark comparisons uninstrumented.

## Benchmarks

A matched Linux comparison on 2026-10-13 measures this implementation (`8351224`),
upstream Go `c4ab53d` and Rust `6dae2fd` on the same physical i9-13900KF desktop.
Three balanced rounds per workload use matching data, clients, settings and complete-response
validation. All 1,190,787 workload writes pass their applicable persistence/FTS/delivery audits.

| Median, three rounds | This implementation | Upstream Go | Rust |
|---|---:|---:|---:|
| POST p99 at 3,000 offered/s, ms | 0.965 | 0.831 | 0.635 |
| POST capacity, requests/s | 9,765.5 | 9,094.9 | 7,155.7 |
| Complete live delivery, messages/s | 3,087.8 | 2,364.3 | 4,351.9 |
| Paced all-client delivery p99, ms | 6.507 | 7.311 | 4.951 |

POST capacity is 7.4% higher than upstream Go and 36.5% higher than Rust; live delivery is
30.6% higher than Go but 29.0% lower than Rust. Fixed-load POST p99 is 16.1% higher than Go
and 52.0% higher than Rust, losing every round to both. These are workload-specific results,
not an overall ranking. Read-route rankings are not refreshed. Three rounds do not establish
a precise confidence interval, and capacity latency at unequal achieved loads is not equal-load latency.

The [validation record](bench/results/owned-operations-20261015/README.md) retains all round
summaries, source/seed/client identities and links to the complete immutable evidence archive.
Its application sources match this branch; no rejected experimental optimization is included.
Historical local/VM timings overlapped other work and are excluded from these claims.
The [shared performance review](https://github.com/basecamp/once-campfire-verification/blob/main/docs/performance-review.md)
uses other hardware and should not be combined with these numbers.

See [`bench/`](bench/) for tooling. Fixed-rate POSTs now validate complete decoded Turbo
receipts, gzip integrity, request identity and sequence before success accounting.
`bench/application` retains acknowledgements and independently matches exact persisted bodies,
client IDs, creators, rooms and FTS rows. Rebuild the client before using this audit.
Latency ends at complete wire-body consumption, before validation; validation and receipt retention
add client work and are not assumed neutral. Match client binaries between comparison arms.

## Known differences

Browser writes use Rust’s token-free Fetch Metadata policy: exact `same-origin` or
`same-site`, a matching Origin when provided, and absent metadata only on plain
HTTP with TLS disabled. Signed disk uploads retain their authenticated capability
exemption; bot-key endpoints exempt authenticated bots only.

- Bundled Turbo 8.0.13 awaits its asynchronous response delegates so cancelled frame-body reads
  reach the existing abort handler; other errors still propagate. The attributed override is in
  `assets/overrides/turbo.js`. Tests require it to match the pinned library with only two added
  `await`s and a license notice. This is an intentional frontend fix, not exact JavaScript parity.

- Session-transfer auto-submit forms explicitly close their form tag; the pinned Rails
  reference omitted it.
- Background sidebar refreshes preserve an open New Ping form and selected recipients.

- Sidebar connection refresh waits for the current Turbo frame to finish loading,
  preventing an aborted response on startup or reconnect. Obsolete connections and removed frames do not reload.

- Bot APIs select actions from the ordered route table, not substrings of the bot key.
  A key beginning with `boosts` no longer turns a message request into a boost request.
- Bot requests with `Content-Type: application/json` validate JSON before writing.
  Malformed JSON returns 400 rather than becoming a message or boost. Nonempty
  whitespace-only JSON is rejected on browser and bot routes. Valid JSON remains
  raw message text unless a non-null scalar attachment parameter selects
  attachment-only attributes; null does not detach an existing attachment.
  Bare query/form keys remain null, distinct from `attachment=`; they no longer
  discard the API's raw message text. Last scalar and query-over-body precedence
  apply to this null metadata as well as to parameter values.
- Outbound compressed responses are decoded only when their bodies are read.
  Push delivery therefore honors 201/404/410 status headers even if an unused
  compressed body is malformed; body-consuming callers still get decoding errors.
- With forwarding disabled, the public listener also discards `Client-IP`. Private
  peers can no longer use that header to spoof the IP used for throttling, bans and
  session records. Explicitly enabled forwarding retains its existing behavior.
- Push registration uses the selected subscription root: outer JSON fields cannot
  fill nested attributes, and a query hash replaces the body hash instead of merging
  its leaves. Rejected registrations no longer store credentials. Multipart nested
  attributes preserve wire order, including direct-upload arrays of metadata hashes.
- HTML and bot JSON message pagination use the last repeated scalar cursor, reference
  integer-prefix casting, and blank-value rules. Invalid or zero cursors return 404
  rather than silently selecting the latest page. A present `before` still selects
  the page even when `after` independently selects the API Link direction.

- Completed gzip uses the selected byte length for GET and HEAD; conditional coding
  metadata no longer depends on cache warmth. Successful avatar/logo files stream
  under file-controller policy rather than acquiring a buffered-body validator,
  and HEAD preserves their file length.

- Private encoding negotiation scans the complete Accept-Encoding field with constant
  state rather than truncating at sixteen items. Late explicit `gzip;q=0` and
  `identity;q=0` exclusions are honored, including after a wildcard. Other private
  tie-breaking rules and the separate public gzip/zstd policy remain unchanged.

- Room involvement updates recheck the current active user and membership inside
  the writer transaction, including retained ping memberships after deactivation.
  Sidebar visibility effects use the committed previous value and have their own
  bounded lifetime rather than being lost to request cancellation.

- Boost publications and bot JSON use the message target and author display captured
  in the boost writer transaction, not earlier HTTP reads. A concurrent profile
  change no longer leaves bot JSON inconsistent with the published boost.

- Search follows current Go/Rust upstream: the newest 100 matching insertion IDs,
  displayed in ID order. Backdated messages can differ from Rails' timestamp order.

- Templates use `html/template`. Whitespace, attribute serialization, some canonical form-action
  URLs, some document titles, and response headers/validators differ from Rust. Strict server/live DOM and network layers
  therefore still fail in many inventory cells, even when screenshots, accessibility and workflows
  match. These failures remain visible in the validation report. Exact protocol parity for malformed
  parameters and every content-negotiation edge case is not claimed.
- WebSockets share serialized and compressed broadcast payloads through a small extension to
  coder/websocket v1.8.15 (see `third_party/websocket/README.campfire`). Outgoing queues hold 256
  frames; slow clients are disconnected. Authorization is checked afresh for each publication,
  batching distinct sessions per room. Rust uses different stream queues.
- Presence refreshes from an absent subscription are ignored; they cannot claim or later
  decrement another tab's presence. Unsubscribe retains its presence contribution until the
  decrement commits; interrupted writes retain the bounded disconnect-cleanup retry.
- Go ignores typing commands for rooms that have been deleted; Rust can still echo them to an
  already subscribed socket. The composer shows the same deleted-room message.
- Empty involvement updates preserve SQL NULL, as in the reference. Go reads that nullable value
  without a scan error; Rust can raise a nil-inquiry error on a subsequent involvement request.
- The response cache uses least-recently-used eviction instead of Rust's sampled eviction. The Go
  message-fragment cache is also independently implemented. It retains versioned message lists
  and owned sidebar frame/layout parts; current membership, permission and layout observations
  are read before cache lookup.
  Room pages also cache their surrounding HTML keyed by fresh page data, inserting freshly selected
  messages and their queried refresh cursor. Search layouts similarly surround a captured immutable
  result body; misses rerun the scoped full query. Complete GET gzip representations have a bounded
  memo keyed by immutable part identities, not by wire ETags. Authorization, sessions and request
  observations stay fresh. Responses derive validators from part lengths and hashes, so ETag values
  differ from both the original Go implementation and Rust. Message pagination uses its
  complete rendered body for ETags, including related-user and boost changes. It omits
  Last-Modified because message timestamps cannot describe external presentation edits.
- Completed room, messages, sidebar and search HTML/gzip responses share a per-process
  cache (`CAMPFIRE_RESPONSE_CACHE_MB`, default 64 MiB, 0 disables it). Every request checks
  its session and access before lookup. A dedicated SQLite reader observes local and foreign
  commits and invalidates whole responses. Message and message-list fragments also carry
  the observed generation and request origin, including external edits without timestamp
  changes and host-dependent rich-text filtering. Request
  variants remain separate; headers and cookies
  stay fresh. GET and HEAD share bodies and preserve validators; flash responses bypass it.
- The default version label and fallback VAPID subject identify `once-campfire-go`. Explicit version,
  VAPID keys and subject settings remain supported.
- With `FORWARD_HEADERS=false`, the public listener strips `X-Forwarded-Ssl` and
  `X-Forwarded-Scheme` along with the other forwarding headers. Unlike the inherited
  behavior, these aliases cannot override the listener's scheme when forwarding is disabled.
- Storage keys containing separators, NUL, or parent-directory shards are rejected before
  filesystem access. Valid keys retain the existing storage layout.
- Media bytes depend on toolchain architecture as well as library versions. Unchanged goldens pass
  with AMD64 FFmpeg 7.1.5 and ARM64 libvips 8.16.1 in the recorded mixed-architecture environment.
  Go and Rust also match a separate native ARM64 Rails capture. A matching version string alone
  does not guarantee matching bytes. Web Push is verified locally, not against external push providers.

Committed attachment processing survives a posting disconnect and has a two-minute pipeline
budget covering media admission, analysis, derivatives and database waits. Analysis and preview
subprocesses retain their 30-second and 60-second limits. Native libvips work cannot always be
preempted. Unread publication and push preparation each have an independent five-second budget;
an unread timeout does not cancel push preparation.

As in Rust, background queues are bounded and in-process: a crash or forced exit can lose queued
jobs. Shutdown stops intake and joins HTTP commands, sockets, presence cleanup and workers before
closing SQLite. Workers are cancelled after a ten-second queue-drain grace; retained purge
continuations then receive a five-second retry window. The CLI allows thirty seconds overall;
if work cannot finish, it exits with status 1 rather than closing shared resources
under surviving work. Native libvips processing cannot always be preempted. See
[the implementation record](plans/go-conversion.md) for coverage and validation scope.

## License

MIT; see [MIT-LICENSE](MIT-LICENSE). Third-party notices for copied/adapted algorithms are in
[licenses](licenses/) and [internal/html/LICENSE](internal/html/LICENSE); module dependencies retain
their own licenses.
