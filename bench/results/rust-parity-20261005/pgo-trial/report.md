# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 7,494 (7,494–7,494) | 1.544 | 4.351 | 8.583 |
| room_show | 16 | rust | 8,270 (8,270–8,270) | 1.838 | 2.707 | 3.833 |
| messages_page | 16 | go | 7,885 (7,885–7,885) | 1.428 | 4.159 | 9.007 |
| messages_page | 16 | rust | 9,557 (9,557–9,557) | 1.613 | 2.277 | 3.021 |
| sidebar | 16 | go | 15,011 (15,011–15,011) | 0.732 | 2.125 | 5.391 |
| sidebar | 16 | rust | 12,858 (12,858–12,858) | 1.200 | 1.736 | 2.283 |
| search | 16 | go | 10,795 (10,795–10,795) | 1.108 | 2.861 | 6.363 |
| search | 16 | rust | 11,452 (11,452–11,452) | 1.296 | 2.003 | 2.931 |
| post_message | 16 | go | 3,543 (3,543–3,543) | 3.375 | 9.311 | 18.815 |
| post_message | 16 | rust | 3,562 (3,562–3,562) | 4.143 | 5.723 | 12.343 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 107.0 | 37.3 | 128.1 | 128.1 | 38.2 |
| rust | not validated | — | 91.0 | 39.4 | 97.3 | 97.3 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
