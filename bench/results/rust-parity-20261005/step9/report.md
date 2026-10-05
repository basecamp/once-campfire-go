# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 7,268 (6,695–7,574) | 1.583 | 4.535 | 9.335 |
| room_show | 16 | rust | 6,548 (6,308–7,513) | 2.255 | 3.587 | 5.099 |
| messages_page | 16 | go | 9,038 (7,225–9,602) | 1.274 | 3.599 | 7.447 |
| messages_page | 16 | rust | 8,450 (7,764–8,519) | 1.797 | 2.699 | 3.905 |
| sidebar | 16 | go | 12,994 (10,421–14,339) | 0.777 | 2.693 | 6.355 |
| sidebar | 16 | rust | 10,249 (7,833–12,862) | 1.472 | 2.261 | 3.407 |
| search | 16 | go | 9,656 (7,488–10,310) | 1.164 | 3.435 | 7.227 |
| search | 16 | rust | 9,927 (9,441–10,309) | 1.492 | 2.351 | 3.513 |
| post_message | 16 | go | 2,070 (1,962–2,906) | 4.675 | 12.879 | 28.959 |
| post_message | 16 | rust | 3,316 (2,692–3,364) | 4.487 | 6.463 | 13.415 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 100.0 | 37.9 | 128.9 | 128.9 | 37.8 |
| rust | not validated | — | 114.3 | 39.2 | 100.1 | 100.1 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
