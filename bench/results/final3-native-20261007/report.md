# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 28,091 (27,843–28,100) | 0.391 | 1.104 | 3.027 |
| room_show | 16 | go | 48,437 (48,229–48,516) | 0.272 | 0.602 | 1.172 |
| room_show | 16 | rust | 31,018 (31,015–31,353) | 0.497 | 0.708 | 0.927 |
| messages_page | 16 | go-before | 28,746 (28,662–28,836) | 0.370 | 1.084 | 3.061 |
| messages_page | 16 | go | 61,132 (61,039–61,197) | 0.192 | 0.462 | 1.436 |
| messages_page | 16 | rust | 35,602 (35,503–35,755) | 0.436 | 0.598 | 0.760 |
| sidebar | 16 | go-before | 31,027 (29,284–31,648) | 0.412 | 0.984 | 1.805 |
| sidebar | 16 | go | 145,829 (145,215–145,952) | 0.084 | 0.195 | 0.463 |
| sidebar | 16 | rust | 40,838 (40,696–40,965) | 0.379 | 0.543 | 0.702 |
| search | 16 | go-before | 27,982 (27,735–28,181) | 0.463 | 1.063 | 1.969 |
| search | 16 | go | 113,670 (112,311–113,826) | 0.107 | 0.246 | 0.567 |
| search | 16 | rust | 32,842 (32,822–32,995) | 0.439 | 0.731 | 1.040 |
| post_message | 16 | go-before | 5,259 (5,207–5,398) | 1.779 | 7.207 | 12.327 |
| post_message | 16 | go | 9,818 (9,761–9,876) | 1.615 | 2.279 | 13.063 |
| post_message | 16 | rust | 7,832 (7,750–7,870) | 1.711 | 3.085 | 5.595 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,260.7 (2,213.1–2,276.5) | 226,075 | 7.119 | 8.247 | not measured |
| 100 | False | go | 5,247.7 (5,231.1–5,291.3) | 524,775 | 2.501 | 3.439 | not measured |
| 100 | False | rust | 4,264.7 (4,225.2–4,295.9) | 426,469 | 3.321 | 4.387 | not measured |
| 100 | True | go-before | 2,423.8 (2,409.9–2,461.3) | 242,375 | 6.639 | 8.719 | 646.1 |
| 100 | True | go | 3,288.6 (3,271.1–3,323.7) | 328,857 | 3.345 | 4.971 | 877.5 |
| 100 | True | rust | 3,365.7 (3,360.5–3,367.5) | 336,574 | 3.813 | 5.311 | 785.6 |
| 1,000 | False | go-before | 301.6 (300.2–303.8) | 301,594 | 51.103 | 105.151 | not measured |
| 1,000 | False | go | 533.8 (521.3–534.2) | 533,772 | 17.519 | 23.679 | not measured |
| 1,000 | False | rust | 550.0 (536.7–551.6) | 550,041 | 20.943 | 35.551 | not measured |
| 1,000 | True | go-before | 297.6 (291.9–302.0) | 297,645 | 46.655 | 103.359 | 792.2 |
| 1,000 | True | go | 330.2 (325.1–332.2) | 330,184 | 27.823 | 38.079 | 878.8 |
| 1,000 | True | rust | 364.1 (361.9–368.4) | 364,112 | 32.799 | 73.663 | 849.4 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 46.89 | 84296 | 26.7 | 33.1 | 129.7 | 260.8 | 38.1 |
| go | 44.65 | 84296 | 26.7 | 37.9 | 140.4 | 290.5 | 40.2 |
| rust | 48.84 | 84296 | 35.8 | 33.3 | 98.3 | 152.6 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 465,761 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
