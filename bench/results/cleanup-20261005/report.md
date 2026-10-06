# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 18,198 (18,114–18,392) | 0.724 | 1.654 | 2.889 |
| room_show | 16 | go | 19,293 (19,289–19,556) | 0.690 | 1.530 | 2.667 |
| room_show | 16 | rust | 20,727 (20,381–21,230) | 0.732 | 1.037 | 1.454 |
| messages_page | 16 | go-before | 19,790 (19,494–19,977) | 0.675 | 1.514 | 2.557 |
| messages_page | 16 | go | 20,407 (20,298–20,638) | 0.653 | 1.435 | 2.483 |
| messages_page | 16 | rust | 21,974 (21,875–22,730) | 0.691 | 0.963 | 1.338 |
| sidebar | 16 | go-before | 21,230 (21,097–21,780) | 0.615 | 1.527 | 2.411 |
| sidebar | 16 | go | 21,967 (21,207–22,098) | 0.595 | 1.468 | 2.379 |
| sidebar | 16 | rust | 26,912 (24,494–27,176) | 0.573 | 0.814 | 1.091 |
| search | 16 | go-before | 23,463 (23,448–24,247) | 0.566 | 1.298 | 2.147 |
| search | 16 | go | 24,025 (23,830–24,689) | 0.554 | 1.267 | 2.079 |
| search | 16 | rust | 26,214 (24,370–26,728) | 0.564 | 0.874 | 1.279 |
| avatar | 16 | go-before | 33,819 (31,408–35,224) | 0.389 | 0.902 | 1.598 |
| avatar | 16 | go | 35,215 (33,668–35,766) | 0.370 | 0.866 | 1.542 |
| avatar | 16 | rust | 35,202 (33,189–35,546) | 0.433 | 0.626 | 0.932 |
| static_css | 16 | go-before | 198,772 (192,929–208,252) | 0.063 | 0.132 | 0.334 |
| static_css | 16 | go | 199,689 (199,059–204,894) | 0.062 | 0.131 | 0.331 |
| static_css | 16 | rust | 250,436 (249,810–251,798) | 0.060 | 0.093 | 0.128 |
| post_message | 16 | go-before | 7,517 (6,485–7,579) | 1.948 | 3.303 | 7.091 |
| post_message | 16 | go | 7,528 (7,493–7,567) | 1.944 | 3.343 | 6.963 |
| post_message | 16 | rust | 7,444 (7,401–7,464) | 1.942 | 2.851 | 7.831 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 32.41 | 84296 | 52.3 | 39.1 | 180.1 | 293.6 | 36.4 |
| go | 36.42 | 84296 | 52.4 | 39.0 | 180.0 | 293.3 | 36.4 |
| rust | 37.48 | 84296 | 36.4 | 42.1 | 130.1 | 253.1 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 451,046 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
