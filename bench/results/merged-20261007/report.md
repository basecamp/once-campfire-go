# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 27,957 (27,750–27,987) | 0.393 | 1.108 | 3.049 |
| room_show | 16 | go | 41,216 (41,020–41,443) | 0.261 | 0.678 | 2.551 |
| room_show | 16 | rust | 30,959 (30,719–31,140) | 0.499 | 0.711 | 0.933 |
| messages_page | 16 | go-before | 28,340 (28,324–28,598) | 0.375 | 1.093 | 3.129 |
| messages_page | 16 | go | 49,632 (49,474–50,095) | 0.211 | 0.544 | 2.313 |
| messages_page | 16 | rust | 35,529 (35,349–35,567) | 0.437 | 0.598 | 0.763 |
| sidebar | 16 | go-before | 31,607 (29,650–34,575) | 0.397 | 0.981 | 1.842 |
| sidebar | 16 | go | 126,859 (126,079–128,003) | 0.097 | 0.225 | 0.580 |
| sidebar | 16 | rust | 40,478 (40,206–40,688) | 0.383 | 0.549 | 0.710 |
| search | 16 | go-before | 27,695 (27,541–28,117) | 0.463 | 1.077 | 2.009 |
| search | 16 | go | 72,759 (71,892–72,770) | 0.154 | 0.377 | 1.282 |
| search | 16 | rust | 32,832 (32,462–32,851) | 0.440 | 0.732 | 1.043 |
| post_message | 16 | go-before | 5,263 (5,228–5,322) | 1.789 | 7.219 | 12.159 |
| post_message | 16 | go | 6,570 (6,569–6,646) | 2.137 | 3.457 | 13.047 |
| post_message | 16 | rust | 7,873 (7,866–7,876) | 1.702 | 3.065 | 5.543 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,264.5 (2,244.1–2,278.5) | 226,450 | 6.603 | 7.527 | not measured |
| 100 | False | go | 3,589.0 (3,571.2–3,683.3) | 358,899 | 3.089 | 3.723 | not measured |
| 100 | False | rust | 4,273.0 (4,266.8–4,296.8) | 427,298 | 3.325 | 4.211 | not measured |
| 100 | True | go-before | 2,428.2 (2,385.6–2,429.6) | 242,821 | 6.751 | 8.815 | 647.3 |
| 100 | True | go | 3,088.4 (3,078.8–3,094.3) | 308,843 | 3.741 | 5.223 | 823.9 |
| 100 | True | rust | 3,346.6 (3,305.1–3,352.9) | 334,657 | 3.887 | 5.367 | 780.9 |
| 1,000 | False | go-before | 303.9 (302.7–310.5) | 303,927 | 47.519 | 70.527 | not measured |
| 1,000 | False | go | 511.3 (510.4–517.5) | 511,344 | 21.599 | 31.839 | not measured |
| 1,000 | False | rust | 545.5 (540.3–550.2) | 545,466 | 21.551 | 34.815 | not measured |
| 1,000 | True | go-before | 304.4 (302.2–305.8) | 304,417 | 37.951 | 65.407 | 809.7 |
| 1,000 | True | go | 328.2 (324.6–330.1) | 328,248 | 32.271 | 49.375 | 873.5 |
| 1,000 | True | rust | 364.3 (363.2–366.8) | 364,347 | 33.759 | 52.575 | 849.7 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 48.25 | 84296 | 27.0 | 34.2 | 131.8 | 260.7 | 38.1 |
| go | 46.21 | 84296 | 26.7 | 35.6 | 134.5 | 302.2 | 39.9 |
| rust | 48.96 | 84296 | 36.1 | 33.1 | 97.7 | 153.5 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 401,414 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
