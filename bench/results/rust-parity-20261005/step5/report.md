# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 5,925 (5,925–5,925) | 2.022 | 5.335 | 10.767 |
| room_show | 16 | rust | 8,376 (8,376–8,376) | 1.831 | 2.663 | 3.625 |
| messages_page | 16 | go | 7,600 (7,600–7,600) | 1.564 | 4.179 | 8.543 |
| messages_page | 16 | rust | 9,382 (9,382–9,382) | 1.642 | 2.337 | 3.133 |
| sidebar | 16 | go | 6,860 (6,860–6,860) | 2.137 | 3.769 | 5.991 |
| sidebar | 16 | rust | 12,914 (12,914–12,914) | 1.198 | 1.727 | 2.275 |
| search | 16 | go | 5,752 (5,752–5,752) | 2.505 | 4.551 | 7.795 |
| search | 16 | rust | 11,581 (11,581–11,581) | 1.291 | 1.970 | 2.855 |
| post_message | 16 | go | 3,471 (3,471–3,471) | 3.409 | 9.631 | 18.751 |
| post_message | 16 | rust | 3,478 (3,478–3,478) | 4.219 | 5.955 | 13.167 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 66.3 | 37.0 | 132.0 | 132.0 | 37.8 |
| rust | not validated | — | 83.1 | 39.8 | 98.6 | 98.6 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
