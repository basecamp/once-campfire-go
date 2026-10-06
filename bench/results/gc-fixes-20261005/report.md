# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 15,046 (14,445–15,340) | 0.872 | 2.057 | 3.467 |
| room_show | 16 | go | 16,233 (15,893–16,324) | 0.825 | 1.863 | 3.073 |
| room_show | 16 | rust | 21,481 (21,296–21,636) | 0.712 | 0.989 | 1.351 |
| messages_page | 16 | go-before | 17,796 (17,494–17,881) | 0.755 | 1.674 | 2.737 |
| messages_page | 16 | go | 17,681 (17,524–17,818) | 0.763 | 1.690 | 2.775 |
| messages_page | 16 | rust | 22,956 (22,177–23,174) | 0.668 | 0.907 | 1.223 |
| sidebar | 16 | go-before | 15,238 (14,897–15,531) | 0.854 | 2.103 | 3.291 |
| sidebar | 16 | go | 17,840 (17,724–17,857) | 0.723 | 1.836 | 2.865 |
| sidebar | 16 | rust | 27,543 (27,387–27,544) | 0.563 | 0.791 | 1.039 |
| search | 16 | go-before | 20,738 (20,431–21,024) | 0.623 | 1.525 | 2.537 |
| search | 16 | go | 22,866 (22,772–23,124) | 0.584 | 1.343 | 2.215 |
| search | 16 | rust | 27,342 (27,215–27,523) | 0.542 | 0.833 | 1.217 |
| avatar | 16 | go-before | 33,549 (33,512–34,351) | 0.393 | 0.914 | 1.537 |
| avatar | 16 | go | 34,015 (33,784–34,078) | 0.386 | 0.899 | 1.570 |
| avatar | 16 | rust | 36,421 (36,412–36,495) | 0.420 | 0.601 | 0.876 |
| static_css | 16 | go-before | 208,518 (206,317–209,949) | 0.061 | 0.125 | 0.302 |
| static_css | 16 | go | 204,066 (198,164–204,147) | 0.062 | 0.128 | 0.324 |
| static_css | 16 | rust | 258,163 (255,329–259,177) | 0.058 | 0.090 | 0.124 |
| post_message | 16 | go-before | 7,420 (7,326–7,506) | 1.972 | 3.419 | 7.083 |
| post_message | 16 | go | 7,379 (7,300–7,440) | 1.977 | 3.457 | 7.347 |
| post_message | 16 | rust | 7,435 (7,428–7,476) | 1.929 | 2.863 | 7.919 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 31.91 | 84296 | 54.4 | 38.8 | 183.0 | 290.1 | 36.4 |
| go | 34.48 | 84296 | 52.8 | 38.8 | 182.0 | 297.8 | 36.4 |
| rust | 90.19 | 84296 | 37.3 | 42.1 | 126.9 | 249.3 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 454,514 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
