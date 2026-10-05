# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 4.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 7,628 (7,628–7,628) | 1.553 | 4.223 | 8.559 |
| room_show | 16 | rust | 8,502 (8,502–8,502) | 1.804 | 2.631 | 3.561 |
| messages_page | 16 | go | 9,257 (9,257–9,257) | 1.256 | 3.457 | 7.391 |
| messages_page | 16 | rust | 8,979 (8,979–8,979) | 1.696 | 2.491 | 3.581 |
| sidebar | 16 | go | 14,996 (14,996–14,996) | 0.735 | 2.127 | 5.375 |
| sidebar | 16 | rust | 12,621 (12,621–12,621) | 1.220 | 1.763 | 2.315 |
| search | 16 | go | 10,833 (10,833–10,833) | 1.131 | 2.865 | 6.007 |
| search | 16 | rust | 10,922 (10,922–10,922) | 1.374 | 2.087 | 2.939 |
| post_message | 16 | go | 3,455 (3,455–3,455) | 3.451 | 9.639 | 18.687 |
| post_message | 16 | rust | 3,750 (3,750–3,750) | 3.901 | 5.459 | 13.295 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 55.5 | 38.2 | 129.9 | 129.9 | 37.8 |
| rust | not validated | — | 86.1 | 39.6 | 102.2 | 102.2 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
