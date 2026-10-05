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
  go test -tags 'sqlite_fts5 media_vectors' ./internal/storage
```

`bin/build` and `bin/check` use mise when Go is absent from PATH and keep temporary build files in
`.cache/`. Live ACME is tested against a local Pebble CA, including restart with the CA offline.
Container verification exercises setup, a live SQLite backup, offline restore, and restart.

## Benchmarks

The [latest comparison](bench/results/concurrency-20261004/README.md) measures the published
Go version, the current Go version and Rust in three rotating runs with identical seed data,
two application CPUs and two SQLite readers each. Median requests/sec at 16 HTTP clients:

| Workload | Previous Go | Current Go | Rust | Current Go / Rust |
|---|---:|---:|---:|---:|
| Room page | 9,456 | 16,924 | 17,324 | 98% |
| Message history | 13,430 | 19,335 | 18,940 | 102% |
| Sidebar (full page) | 11,315 | 16,034 | 20,436 | 78% (66% with Rust's SQLite temp store) |
| Search | 9,073 | 18,800 | 22,942 | 82% |
| Post message | 4,540 | 7,086 | 6,739 | 105% |

Writes now follow the reference's concurrency model: one writer goroutine, WAL checkpoints
beside it, and SQLite work that is not cancelled. The [change log](bench/results/concurrency-20261004/CHANGES.md)
lists each change with its measured effect and tests. Go's p99 latency is still about twice
Rust's on reads. Earlier sidebar comparisons, including the previous report's 24,658 vs 38,303 req/s,
measured Go returning only the sidebar frame where Rust returned
the full page; Go now renders the same page and the sidebar row compares like for like.

These results use warm fragment caches, as both applications do by default. With the
cache disabled in both, Rust renders room pages about 4.4× faster (1,594 vs 362 req/s), so
uncached message rendering remains the largest gap. Action Cable fan-out to 1,000 clients
was unchanged: 233 complete messages/sec for Go and 409 for Rust. Measurements ran in a
4-CPU Docker Desktop VM and are not comparable with the earlier 16-CPU workstation reports
([previous comparison](bench/results/optimization-next-20261003/README.md),
[full-workload comparison](bench/results/application-optimized-20261003/report.md)).

These numbers compare these implementations on this machine, not languages in general.

```sh
# Build both release binaries and Rust's bench/loadgen; prepare its parity seed.
bin/build
bench/application --out bench/results/my-run --reps 3 --seconds 5 \
  --concurrency 1 16 64 --cable-clients 100 1000 10000 --deflate 0 1
```

The harness alternates applications, uses fresh identical seed copies, fixes server/client CPU
sets and four application workers by default (`--workers`), and warms each HTTP workload. It validates message/room IDs,
static/avatar bytes, every successful write and FTS entry, complete Cable fan-out, and actual thumbnail
bytes. Reports include raw samples, source/binary hashes, toolchains, load averages and limitations.
HTTP measurements use the direct application listener and identity encoding; public TLS/compression
throughput is not measured. `bench/health` remains available for the much narrower health-handler test.

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
  message-fragment cache is also independently implemented. It retains versioned message lists;
  current membership and permission data are read before cache lookup. Room, search and sidebar
  pages also cache their HTML keyed by all of their freshly read page data, inserting the current
  messages and refresh timestamp on every request. Responses assemble cached bytes with fresh
  parts and derive validators from part lengths and hashes, so ETag values differ from both the
  original Go implementation and Rust.
- SQLite connections keep temporary B-trees (small `DISTINCT`/`ORDER BY` sorts) in memory
  (`temp_store=MEMORY`); the reference uses SQLite's default file temp store. Results are the
  same. As in the reference, WAL checkpoints run beside a single writer and SQLite work is not
  cancelled when a client goes away.
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
