# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 6,145 (6,145–6,145) | 1.875 | 5.403 | 11.031 |
| room_show | 16 | rust | 8,300 (8,300–8,300) | 1.842 | 2.711 | 3.691 |
| messages_page | 16 | go | 7,721 (7,721–7,721) | 1.484 | 4.251 | 8.735 |
| messages_page | 16 | rust | 9,143 (9,143–9,143) | 1.675 | 2.401 | 3.237 |
| sidebar | 16 | go | 15,243 (15,243–15,243) | 0.728 | 2.093 | 5.187 |
| sidebar | 16 | rust | 12,289 (12,289–12,289) | 1.253 | 1.804 | 2.437 |
| search | 16 | go | 9,138 (9,138–9,138) | 1.325 | 3.383 | 7.111 |
| search | 16 | rust | 10,898 (10,898–10,898) | 1.373 | 2.079 | 3.007 |
| post_message | 16 | go | 3,397 (3,397–3,397) | 3.469 | 9.703 | 19.103 |
| post_message | 16 | rust | 3,681 (3,681–3,681) | 4.059 | 5.411 | 12.383 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 79.3 | 37.6 | 129.3 | 129.3 | 37.8 |
| rust | not validated | — | 76.9 | 39.8 | 102.0 | 102.0 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
