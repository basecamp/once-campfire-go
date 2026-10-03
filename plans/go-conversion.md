# Rust-to-Go implementation record

Port: `once-campfire-go`. Pinned Rust: `64f86353021145b63849fb1cd93adeb08f3b8dbb`.
Pinned Rails: `90b330024dec3e757c79b6a7e6568f93da8e3148`.

The full application is implemented and independently runnable. The implementation uses the standard
library for HTTP/routing, templates, SQL access and process lifecycle, with focused libraries for
SQLite, WebSockets, bcrypt/ACME, HTTP/2 and HTML parsing. It preserves the original frontend and
existing database/storage/cookie formats. The Rust and Rails references are unchanged.

## Coverage

| Area | Implementation and verification |
|---|---|
| Persisted formats | Rails cookies, encryption, signing, IDs/SGIDs, Turbo stream names, Active Storage formats, Ruby coercions/escaping and JSON; golden vectors and cross-binary upgrade check |
| Database | Original schema/migrations, single-writer WAL/read pools, accounts/users/sessions, all room types/memberships, messages/FTS, boosts, bots, push subscriptions, lifecycle callbacks and transactional uploads |
| HTTP | 177 ordered route contracts, authentication/CSRF, setup/join, session transfers, management, browser checks, formats, conditional responses, error pages, Active Storage and ranges |
| Frontend | Original assets/importmap; translated templates for chat, search, management, bots, sessions, PWA and error states; original Playwright screen inventory plus end-to-end workflow tests |
| Rich text | Sanitizing, attachments/mentions, autolink, plain text, editing, malformed HTML limits; all six outputs across 658 reference cases |
| Media | Original signed URLs, layout, checksums, variants, previews, metadata, MIME detection; pinned-toolchain byte goldens and upload-to-real-thumbnail comparison |
| Realtime | Action Cable subscriptions, heartbeats, message/boost/Turbo streams, presence, typing, read/unread, room and session authorization/revocation, bounded queues and compression |
| Integrations/jobs | Bounded queues, Web Push/VAPID, webhook behavior, Open Graph, cleanup; reference vectors and local delivery tests |
| Operations | Production non-root image, HTTP/2/TLS/ACME, H2C, gzip/zstd/jitter, response cache, graceful shutdown, live backups and offline ONCE restore |
| Performance | Identical seed, CPU affinity, alternating repetitions, validated HTTP/write/Cable/media contracts; raw records under bench/results/ |

[route-coverage.json](route-coverage.json) records implemented routes and the deliberately preserved
missing-action error contracts from the reference. A route entry is not a blanket parity claim.
[validation.md](validation.md) records the final checks and screen-layer results. Known differences
are listed in [README.md](../README.md), including strict DOM/network failures; no additional parity
masks or allowlists suppress them.

## Benchmark protocol

Use native release binaries, matching installed media libraries, identical seed/secrets, four
application workers, separate server/load-generator CPU sets, sequential alternating applications,
and at least three repetitions. Each HTTP workload receives a two-second warmup before measurements
at 1/16/64 clients. Cable uses 100/1,000/10,000 clients, with compression off and on. Every timed run
must return successful responses, persist/index every acknowledged write and complete every expected
Cable delivery. Upload measurements fetch the actual thumbnail and compare its bytes.

Report median/range, p50/p90/p99 (the original load generator exposes p90 rather than p95), startup,
Pss, binary sizes and container size. Preserve toolchains, CPU/load, source/binary/seed hashes and raw
samples. Startup is polled at 25 ms; final memory includes allocator high-water effects. Native
direct-listener results do not measure public TLS/compression throughput. Local background workload
is recorded but cannot be eliminated on this shared workstation.

The health-only measurements and early application preflights remain historical evidence, not the
final comparison. The initial application upload selector fetched an avatar; those media timings
are invalidated. The final runner verifies actual representation URLs, image bytes and hashes.
