# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

5 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 14,014 (13,866–14,444) | 0.932 | 2.203 | 3.587 |
| room_show | 16 | go | 19,370 (19,180–19,750) | 0.690 | 1.527 | 2.627 |
| room_show | 16 | rust | 20,927 (19,649–21,558) | 0.726 | 1.032 | 1.425 |
| messages_page | 16 | go-before | 17,066 (16,471–17,265) | 0.782 | 1.756 | 2.833 |
| messages_page | 16 | go | 20,780 (20,338–21,383) | 0.648 | 1.419 | 2.381 |
| messages_page | 16 | rust | 22,494 (20,014–22,943) | 0.681 | 0.935 | 1.279 |
| sidebar | 16 | go-before | 14,106 (13,609–14,464) | 0.917 | 2.283 | 3.583 |
| sidebar | 16 | go | 22,268 (21,969–22,598) | 0.588 | 1.438 | 2.351 |
| sidebar | 16 | rust | 27,055 (25,361–27,668) | 0.571 | 0.808 | 1.081 |
| search | 16 | go-before | 19,925 (19,441–20,565) | 0.645 | 1.607 | 2.703 |
| search | 16 | go | 24,522 (23,760–24,869) | 0.548 | 1.238 | 2.041 |
| search | 16 | rust | 26,047 (24,680–27,083) | 0.568 | 0.881 | 1.292 |
| avatar | 16 | go-before | 32,680 (30,949–33,538) | 0.404 | 0.936 | 1.642 |
| avatar | 16 | go | 35,063 (34,355–35,395) | 0.373 | 0.868 | 1.549 |
| avatar | 16 | rust | 35,459 (33,621–36,273) | 0.427 | 0.626 | 0.964 |
| static_css | 16 | go-before | 198,636 (193,276–201,904) | 0.063 | 0.133 | 0.340 |
| static_css | 16 | go | 198,673 (194,199–205,054) | 0.063 | 0.132 | 0.327 |
| static_css | 16 | rust | 247,738 (241,973–254,747) | 0.060 | 0.094 | 0.131 |
| post_message | 16 | go-before | 7,342 (6,976–7,525) | 2.006 | 3.481 | 6.851 |
| post_message | 16 | go | 7,421 (7,403–7,714) | 1.952 | 3.333 | 6.827 |
| post_message | 16 | rust | 7,315 (6,884–7,457) | 1.977 | 2.903 | 7.787 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 36.71 | 84296 | 53.2 | 38.8 | 180.5 | 295.4 | 36.4 |
| go | 34.07 | 84296 | 52.5 | 37.9 | 182.1 | 295.6 | 36.4 |
| rust | 38.98 | 84296 | 37.0 | 42.1 | 129.4 | 253.0 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

15 completed application runs; 750,950 acknowledged HTTP writes verified in both messages and FTS; 45 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
