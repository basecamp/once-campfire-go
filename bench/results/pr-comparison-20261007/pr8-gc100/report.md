# Campfire application benchmark

Release binaries; public HTTP/1.1 listener; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.

3 rotating repetitions; 10.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-3; load generator on CPUs 4-7. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms | CPU µs/request |
|---|---:|---|---:|---:|---:|---:|---:|
| room_show | 16 | ours | 21,766 (20,848–22,769) | 0.492 | 1.245 | 5.291 | 128.8 |
| room_show | 16 | pr-8 | 4,227 (4,213–4,253) | 3.061 | 7.875 | 12.543 | 937.3 |
| room_show | 16 | rust | 28,093 (27,982–28,590) | 0.486 | 0.904 | 1.867 | 99.0 |
| messages_page | 16 | ours | 21,585 (21,554–22,175) | 0.484 | 1.300 | 5.427 | 133.3 |
| messages_page | 16 | pr-8 | 5,078 (5,075–5,105) | 2.605 | 6.495 | 10.311 | 780.1 |
| messages_page | 16 | rust | 29,643 (29,316–29,950) | 0.465 | 0.841 | 1.804 | 91.2 |
| search | 16 | ours | 20,814 (18,750–20,856) | 0.591 | 1.323 | 4.259 | 128.5 |
| search | 16 | pr-8 | 9,427 (9,402–9,464) | 1.425 | 3.439 | 5.619 | 405.8 |
| search | 16 | rust | 24,987 (24,577–24,995) | 0.588 | 0.941 | 1.342 | 111.1 |
| post_message | 16 | ours | 2,508 (2,384–2,602) | 4.719 | 13.823 | 26.175 | 577.0 |
| post_message | 16 | pr-8 | 2,834 (2,780–2,852) | 5.419 | 8.287 | 12.431 | 703.3 |
| post_message | 16 | rust | 2,986 (2,928–3,175) | 4.907 | 7.263 | 12.615 | 814.3 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| ours | not validated | — | 66.8 | 40.9 | 139.9 | 139.9 | 36.3 |
| pr-8 | not validated | — | 36.2 | 36.2 | 129.1 | 129.1 | 36.4 |
| rust | not validated | — | 75.7 | 41.2 | 123.9 | 123.9 | 35.2 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json.gz) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json.gz) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses the public HTTP listener and gzip encoding. It does not measure TLS/ACME or zstd throughput. Active-room CPU includes the concurrent mutation writer.
- Sidebar room-ID checks do not validate application/Turbo-frame layout completeness; verify that scope separately before any cross-port sidebar comparison.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 298,185 acknowledged HTTP posts and 0 active-room posts verified in messages and FTS; 0 validated thumbnail samples. HTTP errors: 0. Cable throughput was not measured.

Initial full response sizes from the first repetition, before workload timing. Message/room ID and byte contracts are recorded in raw.json. Benchmark contracts do not establish browser or strict HTML parity.

| Response | App | Plain bytes | Encoded bytes | Encoding |
|---|---|---:|---:|---|
| room_show | ours | 374,036 | 21290 | gzip |
| messages_page | ours | 342,444 | 12819 | gzip |
| search | ours | 135,497 | 9463 | gzip |
| room_show | pr-8 | 416,139 | 22083 | gzip |
| messages_page | pr-8 | 383,844 | 13470 | gzip |
| search | pr-8 | 149,625 | 9863 | gzip |
| room_show | rust | 416,139 | 24235 | gzip |
| messages_page | rust | 383,844 | 16158 | gzip |
| search | rust | 149,625 | 9766 | gzip |

Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.
