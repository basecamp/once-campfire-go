# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 5,325 (5,325–5,325) | 2.329 | 5.927 | 11.479 |
| room_show | 16 | rust | 8,558 (8,558–8,558) | 1.794 | 2.629 | 3.577 |
| messages_page | 16 | go | 7,353 (7,353–7,353) | 1.688 | 4.139 | 8.479 |
| messages_page | 16 | rust | 9,476 (9,476–9,476) | 1.621 | 2.293 | 3.057 |
| sidebar | 16 | go | 5,369 (5,369–5,369) | 2.857 | 4.167 | 5.919 |
| sidebar | 16 | rust | 12,775 (12,775–12,775) | 1.214 | 1.747 | 2.265 |
| search | 16 | go | 5,605 (5,605–5,605) | 2.563 | 4.711 | 7.903 |
| search | 16 | rust | 11,149 (11,149–11,149) | 1.352 | 2.049 | 2.853 |
| post_message | 16 | go | 1,877 (1,877–1,877) | 5.395 | 19.711 | 34.463 |
| post_message | 16 | rust | 3,524 (3,524–3,524) | 4.227 | 5.823 | 12.607 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 99.9 | 36.8 | 126.5 | 126.5 | 37.7 |
| rust | not validated | — | 111.1 | 39.8 | 97.6 | 97.6 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
