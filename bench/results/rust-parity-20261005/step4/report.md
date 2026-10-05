# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 5,616 (5,616–5,616) | 2.225 | 5.479 | 10.735 |
| room_show | 16 | rust | 8,417 (8,417–8,417) | 1.822 | 2.651 | 3.547 |
| messages_page | 16 | go | 7,288 (7,288–7,288) | 1.619 | 4.415 | 8.967 |
| messages_page | 16 | rust | 9,090 (9,090–9,090) | 1.699 | 2.391 | 3.161 |
| sidebar | 16 | go | 6,967 (6,967–6,967) | 1.980 | 4.135 | 7.743 |
| sidebar | 16 | rust | 12,620 (12,620–12,620) | 1.227 | 1.753 | 2.291 |
| search | 16 | go | 5,696 (5,696–5,696) | 2.477 | 4.851 | 8.583 |
| search | 16 | rust | 11,124 (11,124–11,124) | 1.335 | 2.069 | 3.055 |
| post_message | 16 | go | 3,472 (3,472–3,472) | 3.389 | 9.535 | 18.911 |
| post_message | 16 | rust | 3,178 (3,178–3,178) | 4.579 | 6.599 | 14.103 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 70.0 | 37.7 | 131.6 | 131.6 | 37.8 |
| rust | not validated | — | 111.5 | 39.3 | 97.9 | 97.9 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
