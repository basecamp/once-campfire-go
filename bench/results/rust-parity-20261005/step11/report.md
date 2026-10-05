# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 7.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 8,248 (8,248–8,248) | 1.368 | 4.035 | 8.215 |
| room_show | 16 | rust | 8,016 (8,016–8,016) | 1.897 | 2.843 | 4.041 |
| messages_page | 16 | go | 9,208 (9,208–9,208) | 1.218 | 3.599 | 7.839 |
| messages_page | 16 | rust | 8,470 (8,470–8,470) | 1.784 | 2.655 | 3.957 |
| sidebar | 16 | go | 13,675 (13,675–13,675) | 0.739 | 2.551 | 6.027 |
| sidebar | 16 | rust | 12,127 (12,127–12,127) | 1.257 | 1.846 | 2.637 |
| search | 16 | go | 10,397 (10,397–10,397) | 1.068 | 3.221 | 6.847 |
| search | 16 | rust | 9,899 (9,899–9,899) | 1.463 | 2.351 | 4.065 |
| post_message | 16 | go | 3,228 (3,228–3,228) | 3.609 | 10.175 | 21.295 |
| post_message | 16 | rust | 2,926 (2,926–2,926) | 4.671 | 7.175 | 15.735 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 70.5 | 38.8 | 133.3 | 133.3 | 37.8 |
| rust | not validated | — | 71.8 | 40.3 | 100.7 | 100.7 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
