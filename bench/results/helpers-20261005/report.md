# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 19,773 (17,990–19,907) | 0.671 | 1.500 | 2.641 |
| room_show | 16 | go | 20,177 (20,107–20,199) | 0.663 | 1.462 | 2.495 |
| room_show | 16 | rust | 21,777 (21,533–21,863) | 0.702 | 0.977 | 1.326 |
| messages_page | 16 | go-before | 21,159 (19,816–21,371) | 0.637 | 1.381 | 2.349 |
| messages_page | 16 | go | 21,521 (21,464–21,651) | 0.623 | 1.356 | 2.291 |
| messages_page | 16 | rust | 23,153 (22,942–23,217) | 0.663 | 0.897 | 1.198 |
| sidebar | 16 | go-before | 21,975 (21,106–21,979) | 0.596 | 1.469 | 2.395 |
| sidebar | 16 | go | 22,508 (22,248–22,585) | 0.583 | 1.428 | 2.311 |
| sidebar | 16 | rust | 27,371 (26,749–27,502) | 0.565 | 0.796 | 1.057 |
| search | 16 | go-before | 24,496 (24,191–24,616) | 0.546 | 1.242 | 2.021 |
| search | 16 | go | 24,742 (24,596–24,817) | 0.543 | 1.229 | 1.977 |
| search | 16 | rust | 27,012 (26,486–27,133) | 0.546 | 0.849 | 1.260 |
| avatar | 16 | go-before | 35,958 (35,930–35,961) | 0.364 | 0.846 | 1.501 |
| avatar | 16 | go | 36,106 (35,924–36,325) | 0.364 | 0.843 | 1.476 |
| avatar | 16 | rust | 36,919 (36,392–37,012) | 0.414 | 0.594 | 0.870 |
| static_css | 16 | go-before | 201,981 (196,523–203,345) | 0.062 | 0.129 | 0.325 |
| static_css | 16 | go | 206,872 (203,802–207,871) | 0.061 | 0.126 | 0.312 |
| static_css | 16 | rust | 256,552 (256,141–257,753) | 0.058 | 0.090 | 0.125 |
| post_message | 16 | go-before | 7,687 (7,600–7,757) | 1.906 | 3.213 | 7.075 |
| post_message | 16 | go | 7,686 (7,681–7,711) | 1.897 | 3.197 | 7.055 |
| post_message | 16 | rust | 7,426 (7,384–7,567) | 1.926 | 2.833 | 7.807 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 32.13 | 84296 | 54.4 | 38.8 | 178.7 | 287.6 | 36.4 |
| go | 33.86 | 84296 | 54.3 | 38.9 | 180.0 | 299.3 | 36.4 |
| rust | 34.26 | 84296 | 36.8 | 42.1 | 128.6 | 249.4 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 468,098 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
