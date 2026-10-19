# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 27,976 (27,852–28,004) | 0.396 | 1.100 | 3.011 |
| room_show | 16 | go | 48,376 (47,766–48,408) | 0.274 | 0.607 | 1.199 |
| room_show | 16 | rust | 31,131 (30,779–31,136) | 0.497 | 0.710 | 0.931 |
| messages_page | 16 | go-before | 28,704 (28,432–28,806) | 0.372 | 1.093 | 3.039 |
| messages_page | 16 | go | 60,427 (59,991–60,944) | 0.194 | 0.461 | 1.468 |
| messages_page | 16 | rust | 35,583 (35,215–35,593) | 0.437 | 0.596 | 0.759 |
| sidebar | 16 | go-before | 30,891 (29,672–31,746) | 0.409 | 0.992 | 1.824 |
| sidebar | 16 | go | 144,538 (144,160–144,691) | 0.085 | 0.197 | 0.481 |
| sidebar | 16 | rust | 40,806 (40,316–40,938) | 0.379 | 0.545 | 0.707 |
| search | 16 | go-before | 27,695 (27,612–28,151) | 0.470 | 1.067 | 1.980 |
| search | 16 | go | 113,246 (111,064–113,436) | 0.108 | 0.246 | 0.573 |
| search | 16 | rust | 32,681 (32,507–32,957) | 0.443 | 0.730 | 1.040 |
| post_message | 16 | go-before | 5,290 (5,264–5,342) | 1.800 | 7.087 | 11.895 |
| post_message | 16 | go | 9,818 (9,650–9,884) | 1.613 | 2.261 | 11.823 |
| post_message | 16 | rust | 7,774 (7,747–7,792) | 1.734 | 3.109 | 5.839 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,221.4 (2,205.1–2,232.6) | 222,140 | 6.859 | 7.891 | not measured |
| 100 | False | go | 3,733.2 (3,728.1–3,758.0) | 373,321 | 7.819 | 13.175 | not measured |
| 100 | False | rust | 4,209.0 (3,556.9–4,272.0) | 420,903 | 3.377 | 4.479 | not measured |
| 100 | True | go-before | 2,414.6 (2,398.0–2,428.8) | 241,461 | 6.631 | 8.599 | 643.9 |
| 100 | True | go | 3,176.0 (3,160.9–3,235.7) | 317,596 | 7.843 | 11.615 | 847.4 |
| 100 | True | rust | 3,339.1 (3,295.6–3,341.7) | 333,913 | 3.859 | 5.491 | 779.2 |
| 1,000 | False | go-before | 308.2 (299.5–309.4) | 308,179 | 51.135 | 78.399 | not measured |
| 1,000 | False | go | 388.7 (388.0–392.9) | 388,680 | 23.519 | 34.591 | not measured |
| 1,000 | False | rust | 544.8 (540.8–558.7) | 544,822 | 21.023 | 36.223 | not measured |
| 1,000 | True | go-before | 299.6 (295.4–301.0) | 299,646 | 43.135 | 81.599 | 797.3 |
| 1,000 | True | go | 302.1 (299.7–303.4) | 302,119 | 31.727 | 52.895 | 803.7 |
| 1,000 | True | rust | 364.1 (359.3–369.2) | 364,091 | 35.071 | 63.551 | 849.2 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 46.31 | 84296 | 26.4 | 33.1 | 129.5 | 256.8 | 38.1 |
| go | 45.07 | 84296 | 26.7 | 37.9 | 139.9 | 296.2 | 40.2 |
| rust | 50.19 | 84296 | 35.8 | 33.0 | 99.6 | 155.1 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 463,937 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 9,462 |
| search | go-before | 135,497 |
| room_show | go | 374,036 |
| messages_page | go | 342,444 |
| sidebar | go | 9,462 |
| search | go | 135,497 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
