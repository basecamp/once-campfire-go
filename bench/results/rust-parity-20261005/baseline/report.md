# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | rust | 8,395 (8,144–9,313) | 1.818 | 2.663 | 3.613 |
| room_show | 16 | go | 4,874 (4,453–5,007) | 2.719 | 6.107 | 11.487 |
| messages_page | 16 | rust | 9,352 (9,153–9,796) | 1.644 | 2.351 | 3.149 |
| messages_page | 16 | go | 5,955 (5,812–6,369) | 2.085 | 5.091 | 9.815 |
| sidebar | 16 | rust | 12,257 (12,058–12,650) | 1.260 | 1.811 | 2.361 |
| sidebar | 16 | go | 5,808 (5,684–6,341) | 2.355 | 4.495 | 8.115 |
| search | 16 | rust | 11,223 (11,207–11,252) | 1.339 | 2.034 | 2.895 |
| search | 16 | go | 5,199 (4,988–5,311) | 2.515 | 5.739 | 10.799 |
| post_message | 16 | rust | 3,524 (3,500–3,588) | 4.235 | 5.755 | 12.239 |
| post_message | 16 | go | 1,900 (1,694–2,002) | 5.643 | 18.495 | 30.703 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| rust | not validated | — | 110.2 | 39.7 | 97.6 | 97.6 | 35.6 |
| go | not validated | — | 77.9 | 33.5 | 124.5 | 124.5 | 37.7 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](https://github.com/nick-potts/once-campfire-go/blob/32c6f7507c75c629e7f0f642a67637e90abeb0e0/bench/results/rust-parity-20261005/baseline/raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](https://github.com/nick-potts/once-campfire-go/blob/32c6f7507c75c629e7f0f642a67637e90abeb0e0/bench/results/rust-parity-20261005/baseline/metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
