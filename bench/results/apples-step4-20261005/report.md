# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 12,432 (11,704–12,466) | 1.077 | 2.423 | 3.947 |
| room_show | 16 | go | 14,888 (14,857–15,122) | 0.874 | 2.071 | 3.397 |
| room_show | 16 | rust | 21,675 (21,653–21,795) | 0.703 | 0.977 | 1.323 |
| messages_page | 16 | go-before | 16,019 (15,021–16,130) | 0.821 | 1.914 | 3.173 |
| messages_page | 16 | go | 17,885 (17,774–17,954) | 0.750 | 1.681 | 2.735 |
| messages_page | 16 | rust | 23,290 (22,971–23,294) | 0.658 | 0.888 | 1.199 |
| sidebar | 16 | go-before | 11,000 (10,741–11,204) | 1.441 | 2.329 | 4.183 |
| sidebar | 16 | go | 14,794 (14,714–15,317) | 0.874 | 2.163 | 3.325 |
| sidebar | 16 | rust | 27,518 (27,484–27,768) | 0.562 | 0.790 | 1.033 |
| search | 16 | go-before | 17,936 (17,452–17,995) | 0.691 | 1.843 | 3.165 |
| search | 16 | go | 20,934 (20,834–20,992) | 0.619 | 1.525 | 2.577 |
| search | 16 | rust | 27,449 (27,170–27,466) | 0.538 | 0.833 | 1.222 |
| avatar | 16 | go-before | 31,388 (30,861–31,401) | 0.407 | 1.007 | 1.788 |
| avatar | 16 | go | 34,315 (34,054–34,364) | 0.382 | 0.897 | 1.544 |
| avatar | 16 | rust | 35,944 (35,608–36,094) | 0.425 | 0.610 | 0.904 |
| static_css | 16 | go-before | 183,345 (164,144–185,825) | 0.063 | 0.154 | 0.373 |
| static_css | 16 | go | 204,248 (201,462–207,908) | 0.061 | 0.129 | 0.320 |
| static_css | 16 | rust | 258,114 (256,448–258,525) | 0.058 | 0.089 | 0.124 |
| post_message | 16 | go-before | 7,244 (7,068–7,406) | 2.014 | 3.635 | 6.931 |
| post_message | 16 | go | 7,374 (7,229–7,475) | 1.993 | 3.471 | 7.055 |
| post_message | 16 | rust | 7,458 (7,398–7,542) | 1.924 | 2.823 | 8.591 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 4,053.2 (3,873.0–4,061.3) | 405,316 | 5.515 | 6.939 | not measured |
| 100 | False | go | 4,050.0 (4,033.4–4,064.3) | 404,995 | 5.047 | 6.827 | not measured |
| 100 | False | rust | 3,755.0 (3,748.5–3,959.7) | 375,498 | 3.995 | 5.795 | not measured |
| 100 | True | go-before | 2,037.0 (2,026.9–2,050.1) | 203,704 | 6.571 | 9.591 | 575.2 |
| 100 | True | go | 2,053.1 (2,047.8–2,057.7) | 205,307 | 6.583 | 9.623 | 579.8 |
| 100 | True | rust | 2,264.6 (2,244.5–2,269.2) | 226,464 | 5.127 | 7.331 | 528.6 |
| 1,000 | False | go-before | 504.4 (495.4–508.1) | 504,388 | 25.583 | 36.383 | not measured |
| 1,000 | False | go | 499.4 (494.3–506.9) | 499,374 | 26.303 | 37.503 | not measured |
| 1,000 | False | rust | 531.9 (524.0–545.6) | 531,921 | 30.799 | 46.591 | not measured |
| 1,000 | True | go-before | 208.3 (208.2–208.7) | 208,294 | 48.319 | 66.559 | 587.9 |
| 1,000 | True | go | 208.0 (207.7–208.4) | 208,008 | 47.199 | 68.159 | 587.0 |
| 1,000 | True | rust | 239.7 (239.2–240.7) | 239,687 | 45.855 | 66.303 | 559.1 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 33.31 | 84296 | 52.2 | 38.6 | 142.5 | 325.2 | 35.7 |
| go | 33.92 | 84296 | 52.0 | 38.8 | 180.7 | 390.0 | 36.4 |
| rust | 32.29 | 84296 | 36.9 | 42.1 | 128.1 | 255.5 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 450,703 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
