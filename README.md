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
messages. Pages, turbo streams and JSON views render byte-identically to the reference: `bench/parity`
compares both applications' responses on one seed for every URL in `bench/parity-urls/`. See
[validation](plans/validation.md) and the known differences below before replacing an installation.
The pinned Rust source is in `reference/`, with its Rails source in `reference/reference/`.

## Dependencies

- Go 1.27.1: `net/http`, `crypto`, `encoding/json`, `embed`, `testing`.
- `github.com/valyala/quicktemplate`: templates compiled to Go. `internal/views` mirrors the
  reference's askama view crate: each template is converted with `bin/askama2qtpl`, which keeps
  every byte of the askama template's text, and its view models, helpers, fragment cache and
  recorded pages (page parts and part-based ETags) are ports of the reference's.
- `crawshaw.io/sqlite`, vendored in `third_party/sqlite` with the reference's SQLite 3.53.2 and
  build options: SQLite's C API through CGO, without `database/sql` (see its `README.campfire`).
  As in the reference, one writer goroutine owns the write connection and runs writes in order,
  WAL checkpoints run on a connection of their own, and reads take one of `RAILS_MAX_THREADS`
  (default 5) reader connections.
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
override individual locations; `RAILS_ENV` defaults to `production`. `campfire db:prepare` initializes
an empty database and checks migration versions; existing databases missing migrations are rejected.

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
  go test -tags media_vectors ./internal/storage
```

`bin/build` and `bin/check` use mise when Go is absent from PATH and keep temporary build files in
`.cache/`. Live ACME is tested against a local Pebble CA, including restart with the CA offline.
Container verification exercises setup, a live SQLite backup, offline restore, and restart.

## Benchmarks

[Apples to apples](bench/results/apples-to-apples-20261005.md) makes Go do the Rust reference's
work — the same SQL, writer and checkpointer, byte-identical pages with the same caches, and the
same Cable fan-out — then takes out, guided by profiles of both applications, what the Go port did
on top of it. Median requests/sec (complete broadcasts/sec for Cable), 16 HTTP clients, three
server CPUs each, the same seed:

| Workload | Published Go | Go | Rust | Rust / Go |
|---|---:|---:|---:|---:|
| Room page | 11,947 | 20,177 | 21,777 | 1.08× |
| Messages page | 16,411 | 21,521 | 23,153 | 1.08× |
| Sidebar | 14,238 | 22,508 | 27,371 | 1.22× |
| Search | 12,101 | 24,742 | 27,012 | 1.09× |
| Post message | 4,699 | 7,686 | 7,426 | 0.97× |
| Cable, 1,000 clients | 261 | 499 | 532 | 1.07× |

The published version's pages did less work (a cached page shell, a 9 KB sidebar frame where
Rust sends a 30 KB page), so only the last three columns compare like with like; Cable is from
step 4, which the later steps don't touch. The remaining gap is garbage collection (`GOGC=200` by
default; Go uses 180 MiB after the HTTP phase to Rust's 129), about 80 cgo calls per page into
SQLite, and net/http's own work; the summary breaks it down per page. The per-step reports are
`bench/results/apples-step{1,2,3,4}-20261005` and, for the profile-guided steps 5–9,
`bench/results/{gc-fixes,cgo-rows,sidebar,cleanup,helpers}-20261005`; earlier optimization passes
are in `bench/results/optimization-*`. `go build` uses profile-guided optimization from
`cmd/campfire/default.pgo`, merged from `bench/profile` CPU profiles of the room, messages,
sidebar, search, avatar, write and Cable workloads; regenerate it after significant changes.

These numbers compare these implementations on this workstation, not languages in general.

```sh
# Build both release binaries and Rust's bench/loadgen; prepare its parity seed.
bin/build
bench/application --out bench/results/my-run --reps 3 --seconds 5 \
  --concurrency 1 16 64 --cable-clients 100 1000 10000 --deflate 0 1
```

The harness alternates applications, uses fresh identical seed copies, fixes server/client CPU
sets and four application workers, and warms each HTTP workload. It validates message/room IDs,
static/avatar bytes, every successful write and FTS entry, complete Cable fan-out, and actual thumbnail
bytes. Reports include raw samples, source/binary hashes, toolchains, load averages and limitations.
HTTP measurements use the direct application listener and identity encoding; public TLS/compression
throughput is not measured. `bench/health` remains available for the much narrower health-handler test.

## Known differences

- Response headers can differ where the reference's kit and Go's net/http differ: Go adds no `Vary:
  Accept-Encoding` to empty bodies, and net/http writes a page's parts from one buffer rather than
  with vectored writes. Exact protocol parity for malformed parameters and every content-negotiation
  edge case is not claimed.
- Action Cable follows Rust's design: broadcasts are routed by stream to the subscribers sharing an
  identifier, as one shared frame compressed at most once; channels authorize on subscribe, and the
  same writes as in Rust (ban, deactivation, sign-out, losing a room membership) disconnect the
  user's sockets. Frames go out up to 64 per vectored write through a small extension to
  coder/websocket v1.8.15 (see `third_party/websocket/README.campfire`). Rust buffers each stream
  once in a shared ring; Go queues per connection, but closes a connection at the same point (a
  subscription 256 frames behind). Room message streams are keyed by room id rather than by the
  signed GlobalID stream name.
- The public front server's response cache uses least-recently-used eviction instead of Rust's
  sampled eviction.
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
