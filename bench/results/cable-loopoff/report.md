# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 2.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 47,462 (47,462–47,462) | 0.247 | 0.609 | 1.912 |
| room_show | 16 | rust | 31,026 (31,026–31,026) | 0.496 | 0.707 | 0.936 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 1,000 | False | go | 517.8 (517.8–517.8) | 517,810 | 17.663 | 24.943 | not measured |
| 1,000 | False | rust | 548.7 (548.7–548.7) | 548,665 | 21.727 | 41.055 | not measured |
| 1,000 | True | go | 329.8 (329.8–329.8) | 329,845 | 28.703 | 39.135 | 877.9 |
| 1,000 | True | rust | 365.2 (365.2–365.2) | 365,165 | 32.287 | 50.815 | 851.8 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | 46.91 | 84296 | 52.8 | 35.7 | 55.2 | 252.3 | 0.0 |
| rust | 50.27 | 84296 | 36.4 | 33.1 | 39.4 | 146.9 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
