# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 16,386 (16,234–17,052) | 0.821 | 1.834 | 2.963 |
| room_show | 16 | go | 18,254 (18,045–18,330) | 0.725 | 1.651 | 2.881 |
| room_show | 16 | rust | 21,688 (21,581–21,726) | 0.706 | 0.977 | 1.334 |
| messages_page | 16 | go-before | 17,769 (17,658–18,636) | 0.752 | 1.677 | 2.755 |
| messages_page | 16 | go | 20,155 (20,040–20,203) | 0.662 | 1.473 | 2.523 |
| messages_page | 16 | rust | 23,133 (22,906–23,155) | 0.662 | 0.897 | 1.210 |
| sidebar | 16 | go-before | 18,056 (17,770–18,535) | 0.721 | 1.791 | 2.835 |
| sidebar | 16 | go | 19,577 (19,442–19,882) | 0.650 | 1.688 | 2.635 |
| sidebar | 16 | rust | 27,565 (27,539–27,735) | 0.561 | 0.787 | 1.036 |
| search | 16 | go-before | 23,108 (22,842–23,509) | 0.580 | 1.334 | 2.163 |
| search | 16 | go | 24,270 (24,054–24,354) | 0.549 | 1.258 | 2.083 |
| search | 16 | rust | 27,224 (27,119–27,574) | 0.541 | 0.840 | 1.233 |
| avatar | 16 | go-before | 34,248 (34,009–34,815) | 0.386 | 0.894 | 1.535 |
| avatar | 16 | go | 34,969 (34,963–35,016) | 0.374 | 0.874 | 1.534 |
| avatar | 16 | rust | 36,140 (36,004–36,624) | 0.421 | 0.608 | 0.900 |
| static_css | 16 | go-before | 205,918 (202,877–208,688) | 0.061 | 0.127 | 0.310 |
| static_css | 16 | go | 205,620 (201,157–208,756) | 0.061 | 0.127 | 0.311 |
| static_css | 16 | rust | 259,169 (257,140–259,720) | 0.058 | 0.089 | 0.123 |
| post_message | 16 | go-before | 7,475 (7,446–7,485) | 1.961 | 3.385 | 7.283 |
| post_message | 16 | go | 7,574 (7,563–7,631) | 1.922 | 3.317 | 7.271 |
| post_message | 16 | rust | 7,471 (7,347–7,496) | 1.944 | 2.855 | 7.847 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 34.70 | 84296 | 51.9 | 38.1 | 178.1 | 297.5 | 36.4 |
| go | 32.26 | 84296 | 53.0 | 38.8 | 184.0 | 299.0 | 36.4 |
| rust | 32.86 | 84296 | 37.4 | 42.1 | 126.0 | 252.7 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 461,579 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
