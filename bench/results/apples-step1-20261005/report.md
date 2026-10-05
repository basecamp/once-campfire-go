# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 11,947 (11,066–12,006) | 1.126 | 2.473 | 4.175 |
| room_show | 16 | go | 13,579 (12,812–13,704) | 0.922 | 2.439 | 4.013 |
| room_show | 16 | rust | 21,097 (21,043–21,121) | 0.725 | 1.013 | 1.383 |
| messages_page | 16 | go-before | 16,411 (16,334–16,876) | 0.767 | 1.846 | 3.347 |
| messages_page | 16 | go | 18,625 (18,295–19,747) | 0.645 | 1.800 | 3.199 |
| messages_page | 16 | rust | 22,196 (21,911–22,242) | 0.687 | 0.948 | 1.331 |
| sidebar | 16 | go-before | 14,238 (13,325–14,496) | 0.987 | 1.873 | 3.243 |
| sidebar | 16 | go | 21,849 (21,826–22,022) | 0.593 | 1.460 | 2.559 |
| sidebar | 16 | rust | 26,453 (26,373–27,170) | 0.583 | 0.830 | 1.112 |
| search | 16 | go-before | 12,101 (12,069–12,466) | 1.095 | 2.491 | 3.977 |
| search | 16 | go | 13,937 (13,518–14,151) | 0.894 | 2.401 | 3.915 |
| search | 16 | rust | 26,647 (25,244–26,856) | 0.555 | 0.858 | 1.275 |
| avatar | 16 | go-before | 27,559 (26,826–27,853) | 0.480 | 1.059 | 2.109 |
| avatar | 16 | go | 31,336 (31,118–31,442) | 0.409 | 0.986 | 1.913 |
| avatar | 16 | rust | 36,182 (35,932–36,369) | 0.422 | 0.605 | 0.888 |
| static_css | 16 | go-before | 188,908 (175,163–193,753) | 0.063 | 0.145 | 0.357 |
| static_css | 16 | go | 192,455 (183,933–193,611) | 0.064 | 0.142 | 0.346 |
| static_css | 16 | rust | 257,192 (244,933–257,586) | 0.058 | 0.090 | 0.126 |
| post_message | 16 | go-before | 4,699 (4,655–4,885) | 1.897 | 8.423 | 13.423 |
| post_message | 16 | go | 7,359 (7,328–7,502) | 1.930 | 3.799 | 6.815 |
| post_message | 16 | rust | 7,452 (7,253–7,454) | 1.950 | 2.917 | 7.379 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 1,793.1 (1,730.2–1,804.4) | 179,312 | 10.679 | 14.311 | not measured |
| 100 | False | go | 2,176.5 (2,110.5–2,204.3) | 217,646 | 12.823 | 20.575 | not measured |
| 100 | False | rust | 3,785.3 (3,697.7–3,812.7) | 378,528 | 3.233 | 5.503 | not measured |
| 100 | True | go-before | 1,736.3 (1,714.3–1,749.0) | 173,626 | 9.735 | 13.599 | 462.7 |
| 100 | True | go | 1,939.2 (1,915.4–1,953.6) | 193,918 | 9.863 | 14.639 | 513.3 |
| 100 | True | rust | 2,265.5 (2,256.5–2,266.9) | 226,546 | 4.927 | 6.843 | 528.8 |
| 1,000 | False | go-before | 261.4 (257.0–268.9) | 261,411 | 118.015 | 179.839 | not measured |
| 1,000 | False | go | 277.2 (273.6–282.9) | 277,164 | 133.887 | 211.583 | not measured |
| 1,000 | False | rust | 531.4 (525.8–532.6) | 531,406 | 29.151 | 42.623 | not measured |
| 1,000 | True | go-before | 211.9 (210.0–212.8) | 211,902 | 48.895 | 73.471 | 564.2 |
| 1,000 | True | go | 210.9 (210.5–211.1) | 210,875 | 50.335 | 76.671 | 557.7 |
| 1,000 | True | rust | 237.3 (236.1–237.7) | 237,346 | 46.879 | 69.567 | 553.5 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 31.44 | 84296 | 52.5 | 40.2 | 153.6 | 355.7 | 36.2 |
| go | 31.21 | 84296 | 52.4 | 40.6 | 156.4 | 354.9 | 37.1 |
| rust | 34.42 | 84296 | 37.0 | 42.1 | 125.7 | 252.3 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 400,916 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 9,462 |
| search | go-before | 135,497 |
| avatar | go-before | 3,364 |
| static_css | go-before | 1,218 |
| room_show | go | 374,036 |
| messages_page | go | 342,444 |
| sidebar | go | 9,462 |
| search | go | 135,497 |
| avatar | go | 3,364 |
| static_css | go | 1,218 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |
| avatar | rust | 3,364 |
| static_css | rust | 1,218 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
