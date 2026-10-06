# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 17,648 (17,430–17,690) | 0.747 | 1.705 | 2.949 |
| room_show | 16 | go | 16,475 (15,974–18,064) | 0.780 | 1.839 | 3.145 |
| room_show | 16 | rust | 20,376 (16,957–21,059) | 0.743 | 1.062 | 1.500 |
| messages_page | 16 | go-before | 19,129 (17,993–19,796) | 0.693 | 1.558 | 2.711 |
| messages_page | 16 | go | 19,619 (19,616–19,735) | 0.676 | 1.507 | 2.631 |
| messages_page | 16 | rust | 21,482 (18,253–22,413) | 0.704 | 0.984 | 1.442 |
| sidebar | 16 | go-before | 19,175 (18,978–19,287) | 0.660 | 1.723 | 2.743 |
| sidebar | 16 | go | 21,156 (19,974–21,504) | 0.611 | 1.519 | 2.545 |
| sidebar | 16 | rust | 27,029 (26,858–27,199) | 0.572 | 0.812 | 1.083 |
| search | 16 | go-before | 23,528 (23,289–23,760) | 0.562 | 1.303 | 2.249 |
| search | 16 | go | 22,561 (22,487–23,414) | 0.579 | 1.353 | 2.353 |
| search | 16 | rust | 26,150 (25,117–26,818) | 0.567 | 0.877 | 1.270 |
| avatar | 16 | go-before | 34,690 (34,266–35,300) | 0.381 | 0.881 | 1.517 |
| avatar | 16 | go | 33,300 (24,641–34,428) | 0.387 | 0.906 | 1.765 |
| avatar | 16 | rust | 35,088 (32,894–35,605) | 0.432 | 0.634 | 0.980 |
| static_css | 16 | go-before | 202,065 (197,322–203,120) | 0.062 | 0.130 | 0.325 |
| static_css | 16 | go | 141,297 (130,865–199,890) | 0.070 | 0.184 | 0.776 |
| static_css | 16 | rust | 251,494 (119,815–254,114) | 0.060 | 0.092 | 0.129 |
| post_message | 16 | go-before | 7,151 (6,602–7,393) | 2.040 | 3.553 | 7.439 |
| post_message | 16 | go | 7,237 (4,304–7,590) | 2.005 | 3.525 | 6.863 |
| post_message | 16 | rust | 7,226 (6,404–7,275) | 1.989 | 2.967 | 7.799 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 34.62 | 84296 | 52.8 | 37.1 | 180.2 | 291.9 | 36.4 |
| go | 37.22 | 84296 | 52.3 | 39.0 | 178.4 | 293.5 | 36.4 |
| rust | 36.11 | 84296 | 63.3 | 42.1 | 125.9 | 256.7 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 418,997 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
