# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

5 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 12,216 (11,729–12,810) | 1.096 | 2.431 | 4.037 |
| room_show | 16 | go | 19,896 (18,915–19,978) | 0.675 | 1.484 | 2.553 |
| room_show | 16 | rust | 21,417 (21,232–21,500) | 0.714 | 0.997 | 1.365 |
| messages_page | 16 | go-before | 17,124 (16,572–17,894) | 0.751 | 1.774 | 3.061 |
| messages_page | 16 | go | 21,212 (20,081–21,280) | 0.633 | 1.392 | 2.433 |
| messages_page | 16 | rust | 22,783 (22,428–22,942) | 0.669 | 0.917 | 1.256 |
| sidebar | 16 | go-before | 15,644 (14,584–16,098) | 0.928 | 1.671 | 2.727 |
| sidebar | 16 | go | 22,252 (22,178–22,564) | 0.589 | 1.435 | 2.351 |
| sidebar | 16 | rust | 27,161 (26,794–27,552) | 0.568 | 0.805 | 1.072 |
| search | 16 | go-before | 12,587 (12,486–13,104) | 1.068 | 2.377 | 3.759 |
| search | 16 | go | 24,687 (24,316–24,988) | 0.544 | 1.225 | 2.015 |
| search | 16 | rust | 26,888 (26,665–27,144) | 0.550 | 0.855 | 1.252 |
| avatar | 16 | go-before | 27,596 (27,444–28,305) | 0.475 | 1.056 | 2.099 |
| avatar | 16 | go | 35,742 (34,636–35,878) | 0.367 | 0.851 | 1.517 |
| avatar | 16 | rust | 36,289 (35,513–36,771) | 0.422 | 0.605 | 0.890 |
| static_css | 16 | go-before | 193,408 (183,736–193,972) | 0.063 | 0.141 | 0.342 |
| static_css | 16 | go | 201,217 (199,644–202,102) | 0.062 | 0.130 | 0.332 |
| static_css | 16 | rust | 254,560 (251,409–257,906) | 0.059 | 0.091 | 0.126 |
| post_message | 16 | go-before | 4,782 (4,763–4,858) | 1.862 | 8.311 | 13.143 |
| post_message | 16 | go | 7,646 (7,554–7,665) | 1.922 | 3.287 | 6.627 |
| post_message | 16 | rust | 7,453 (7,245–7,546) | 1.947 | 2.859 | 7.599 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 34.37 | 84296 | 52.2 | 40.2 | 151.9 | 265.0 | 36.2 |
| go | 32.70 | 84296 | 51.8 | 36.9 | 181.8 | 297.2 | 36.4 |
| rust | 35.27 | 84296 | 37.6 | 42.1 | 129.8 | 255.3 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

15 completed application runs; 679,292 acknowledged HTTP writes verified in both messages and FTS; 45 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 9,462 |
| search | go-before | 135,497 |
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
