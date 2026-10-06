# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 14,446 (14,123–14,965) | 0.907 | 2.125 | 3.513 |
| room_show | 16 | go | 19,054 (18,124–19,958) | 0.703 | 1.554 | 2.607 |
| room_show | 16 | rust | 21,182 (20,374–21,369) | 0.718 | 1.008 | 1.427 |
| messages_page | 16 | go-before | 17,185 (15,726–17,205) | 0.781 | 1.747 | 2.871 |
| messages_page | 16 | go | 19,322 (19,107–21,538) | 0.693 | 1.531 | 2.519 |
| messages_page | 16 | rust | 22,768 (22,700–22,772) | 0.672 | 0.919 | 1.245 |
| sidebar | 16 | go-before | 14,781 (13,430–15,102) | 0.879 | 2.163 | 3.313 |
| sidebar | 16 | go | 21,616 (21,242–22,335) | 0.603 | 1.477 | 2.461 |
| sidebar | 16 | rust | 27,160 (26,919–27,275) | 0.568 | 0.802 | 1.071 |
| search | 16 | go-before | 20,360 (18,588–20,778) | 0.632 | 1.585 | 2.631 |
| search | 16 | go | 24,456 (24,108–25,000) | 0.549 | 1.242 | 2.012 |
| search | 16 | rust | 26,981 (26,652–27,034) | 0.548 | 0.846 | 1.259 |
| avatar | 16 | go-before | 32,938 (32,768–34,139) | 0.401 | 0.931 | 1.611 |
| avatar | 16 | go | 35,308 (34,438–35,662) | 0.373 | 0.863 | 1.534 |
| avatar | 16 | rust | 35,172 (34,162–36,512) | 0.433 | 0.625 | 0.925 |
| static_css | 16 | go-before | 201,099 (191,912–207,304) | 0.062 | 0.131 | 0.331 |
| static_css | 16 | go | 202,509 (199,590–207,000) | 0.061 | 0.129 | 0.324 |
| static_css | 16 | rust | 254,764 (213,286–257,319) | 0.059 | 0.091 | 0.129 |
| post_message | 16 | go-before | 7,232 (6,905–7,469) | 2.038 | 3.577 | 6.831 |
| post_message | 16 | go | 7,613 (7,550–7,736) | 1.921 | 3.259 | 6.655 |
| post_message | 16 | rust | 7,255 (6,738–7,405) | 1.991 | 2.977 | 7.675 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 3,998.7 (3,972.3–4,207.1) | 399,871 | 5.219 | 6.647 | not measured |
| 100 | False | go | 3,965.4 (3,796.6–4,226.7) | 396,536 | 5.359 | 6.651 | not measured |
| 100 | False | rust | 3,770.7 (3,452.2–3,946.4) | 377,068 | 4.523 | 6.115 | not measured |
| 100 | True | go-before | 2,051.8 (2,050.9–2,054.3) | 205,178 | 6.491 | 9.415 | 579.4 |
| 100 | True | go | 2,050.4 (2,041.9–2,070.1) | 205,038 | 6.243 | 8.951 | 579.0 |
| 100 | True | rust | 2,273.9 (2,238.9–2,294.6) | 227,395 | 5.363 | 7.711 | 530.5 |
| 1,000 | False | go-before | 496.1 (481.8–506.8) | 496,095 | 25.343 | 36.415 | not measured |
| 1,000 | False | go | 509.0 (498.4–511.6) | 508,960 | 25.759 | 36.159 | not measured |
| 1,000 | False | rust | 536.2 (530.1–549.8) | 536,173 | 27.727 | 40.799 | not measured |
| 1,000 | True | go-before | 208.2 (204.9–210.2) | 208,185 | 49.887 | 73.663 | 587.6 |
| 1,000 | True | go | 208.5 (203.2–209.5) | 208,524 | 46.239 | 68.927 | 588.4 |
| 1,000 | True | rust | 237.6 (237.3–240.6) | 237,550 | 46.463 | 74.239 | 553.9 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 32.21 | 84296 | 52.6 | 39.1 | 179.8 | 397.9 | 36.4 |
| go | 32.80 | 84296 | 51.9 | 39.0 | 179.8 | 395.2 | 36.4 |
| rust | 33.92 | 84296 | 37.1 | 42.1 | 129.4 | 257.9 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 446,752 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 416,139 |
| messages_page | go-before | 383,844 |
| sidebar | go-before | 30,763 |
| search | go-before | 149,625 |
| avatar | go-before | 3,364 |
| static_css | go-before | 1,218 |
| room_show | go | 416,139 |
| messages_page | go | 383,844 |
| sidebar | go | 30,763 |
| search | go | 149,625 |
| avatar | go | 3,364 |
| static_css | go | 1,218 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |
| avatar | rust | 3,364 |
| static_css | rust | 1,218 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
