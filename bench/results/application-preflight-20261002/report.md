# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 0.1-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 1 | rust | 5,406 (5,406–5,406) | 0.176 | 0.211 | 0.309 |
| room_show | 1 | go | 855 (855–855) | 1.149 | 1.350 | 1.492 |
| messages_page | 1 | rust | 6,551 (6,551–6,551) | 0.127 | 0.207 | 0.330 |
| messages_page | 1 | go | 1,799 (1,799–1,799) | 0.548 | 0.711 | 0.832 |
| sidebar | 1 | rust | 9,517 (9,517–9,517) | 0.103 | 0.109 | 0.122 |
| sidebar | 1 | go | 358 (358–358) | 2.687 | 3.075 | 3.173 |
| search | 1 | rust | 8,895 (8,895–8,895) | 0.103 | 0.126 | 0.155 |
| search | 1 | go | 2,180 (2,180–2,180) | 0.425 | 0.569 | 0.682 |
| avatar | 1 | rust | 14,202 (14,202–14,202) | 0.068 | 0.077 | 0.090 |
| avatar | 1 | go | 2,482 (2,482–2,482) | 0.396 | 0.414 | 0.526 |
| static_css | 1 | rust | 71,770 (71,770–71,770) | 0.013 | 0.013 | 0.021 |
| static_css | 1 | go | 62,397 (62,397–62,397) | 0.014 | 0.017 | 0.030 |
| post_message | 1 | rust | 3,762 (3,762–3,762) | 0.225 | 0.288 | 1.845 |
| post_message | 1 | go | 1,012 (1,012–1,012) | 0.918 | 1.021 | 1.740 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | rust | 3,841 (3,841–3,841) | 384,142 | 3.241 | 3.907 | not measured |
| 100 | False | go | 563 (563–563) | 56,279 | 13.439 | 16.095 | not measured |
| 100 | True | rust | 3,376 (3,376–3,376) | 337,555 | 3.681 | 5.159 | 787.2 |
| 100 | True | go | 447 (447–447) | 44,712 | 18.991 | 26.271 | 117.9 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| rust | 41.34 | 84296 | 40.0 | 40.6 | 101.8 | 203.8 | 35.8 |
| go | 39.31 | 84296 | 202.6 | 36.1 | 120.7 | 250.9 | 37.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.
