# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 3.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 8,085 (8,085–8,085) | 1.411 | 4.039 | 8.487 |
| room_show | 16 | rust | 8,523 (8,523–8,523) | 1.798 | 2.647 | 3.639 |
| messages_page | 16 | go | 7,892 (7,892–7,892) | 1.471 | 4.127 | 8.271 |
| messages_page | 16 | rust | 9,761 (9,761–9,761) | 1.574 | 2.251 | 2.995 |
| sidebar | 16 | go | 15,252 (15,252–15,252) | 0.735 | 2.081 | 5.043 |
| sidebar | 16 | rust | 12,830 (12,830–12,830) | 1.207 | 1.723 | 2.261 |
| search | 16 | go | 10,879 (10,879–10,879) | 1.128 | 2.785 | 6.159 |
| search | 16 | rust | 11,594 (11,594–11,594) | 1.294 | 1.960 | 2.791 |
| post_message | 16 | go | 3,488 (3,488–3,488) | 3.403 | 9.511 | 18.847 |
| post_message | 16 | rust | 3,768 (3,768–3,768) | 3.937 | 5.235 | 12.319 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | not validated | — | 66.5 | 37.8 | 128.4 | 128.4 | 37.8 |
| rust | not validated | — | 109.4 | 39.6 | 100.1 | 100.1 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
