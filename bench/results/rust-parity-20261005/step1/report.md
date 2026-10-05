# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 5,529 (5,529–5,529) | 2.231 | 5.619 | 11.103 |
| room_show | 16 | rust | 8,553 (8,553–8,553) | 1.791 | 2.603 | 3.553 |
| messages_page | 16 | go | 6,934 (6,934–6,934) | 1.816 | 4.431 | 8.487 |
| messages_page | 16 | rust | 9,038 (9,038–9,038) | 1.705 | 2.415 | 3.207 |
| sidebar | 16 | go | 6,572 (6,572–6,572) | 2.239 | 3.825 | 6.827 |
| sidebar | 16 | rust | 13,273 (13,273–13,273) | 1.160 | 1.684 | 2.235 |
| search | 16 | go | 6,614 (6,614–6,614) | 2.020 | 4.371 | 8.147 |
| search | 16 | rust | 11,538 (11,538–11,538) | 1.297 | 1.968 | 2.861 |
| post_message | 16 | go | 1,835 (1,835–1,835) | 5.839 | 19.087 | 34.399 |
| post_message | 16 | rust | 3,433 (3,433–3,433) | 4.343 | 5.959 | 12.791 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 82.3 | 34.3 | 129.1 | 129.1 | 37.7 |
| rust | not validated | — | 113.4 | 39.3 | 99.3 | 99.3 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
