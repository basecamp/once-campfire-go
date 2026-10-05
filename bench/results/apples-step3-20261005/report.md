# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 12,277 (12,165–12,818) | 1.084 | 2.481 | 3.919 |
| room_show | 16 | go | 12,509 (11,816–12,536) | 1.064 | 2.399 | 4.187 |
| room_show | 16 | rust | 21,320 (21,295–21,364) | 0.714 | 1.002 | 1.378 |
| messages_page | 16 | go-before | 15,655 (15,406–16,240) | 0.840 | 1.964 | 3.195 |
| messages_page | 16 | go | 15,083 (14,484–15,740) | 0.871 | 2.044 | 3.395 |
| messages_page | 16 | rust | 22,375 (21,654–22,617) | 0.684 | 0.940 | 1.297 |
| sidebar | 16 | go-before | 11,131 (10,828–11,661) | 1.430 | 2.299 | 4.075 |
| sidebar | 16 | go | 10,480 (10,368–11,653) | 1.476 | 2.497 | 4.763 |
| sidebar | 16 | rust | 26,601 (26,498–27,261) | 0.575 | 0.827 | 1.130 |
| search | 16 | go-before | 17,657 (16,958–17,745) | 0.703 | 1.872 | 3.097 |
| search | 16 | go | 17,868 (17,092–18,458) | 0.703 | 1.818 | 3.133 |
| search | 16 | rust | 26,665 (26,506–26,734) | 0.555 | 0.861 | 1.250 |
| avatar | 16 | go-before | 30,576 (30,272–30,819) | 0.416 | 1.038 | 1.806 |
| avatar | 16 | go | 29,621 (27,966–31,715) | 0.430 | 1.060 | 1.900 |
| avatar | 16 | rust | 35,929 (35,057–36,156) | 0.423 | 0.613 | 0.948 |
| static_css | 16 | go-before | 182,872 (181,355–183,178) | 0.064 | 0.153 | 0.375 |
| static_css | 16 | go | 181,886 (177,080–189,541) | 0.063 | 0.155 | 0.381 |
| static_css | 16 | rust | 253,801 (247,355–254,940) | 0.059 | 0.091 | 0.127 |
| post_message | 16 | go-before | 7,348 (6,973–7,365) | 1.977 | 3.429 | 7.319 |
| post_message | 16 | go | 7,155 (6,516–7,223) | 2.041 | 3.659 | 6.927 |
| post_message | 16 | rust | 7,437 (7,405–7,452) | 1.952 | 2.865 | 7.763 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,150.9 (1,965.2–2,242.5) | 215,085 | 14.255 | 22.639 | not measured |
| 100 | False | go | 3,960.0 (3,612.7–3,969.9) | 396,001 | 5.483 | 6.979 | not measured |
| 100 | False | rust | 3,837.4 (3,828.3–3,851.6) | 383,744 | 3.379 | 5.791 | not measured |
| 100 | True | go-before | 1,913.1 (1,852.0–1,921.5) | 191,306 | 9.303 | 13.951 | 540.3 |
| 100 | True | go | 2,015.7 (1,969.2–2,033.7) | 201,571 | 7.227 | 10.447 | 569.3 |
| 100 | True | rust | 2,274.6 (2,261.2–2,295.1) | 227,460 | 5.031 | 7.307 | 530.8 |
| 1,000 | False | go-before | 259.7 (259.2–278.3) | 259,687 | 127.807 | 223.231 | not measured |
| 1,000 | False | go | 494.9 (444.3–502.2) | 494,931 | 26.527 | 37.951 | not measured |
| 1,000 | False | rust | 535.2 (523.8–543.3) | 535,178 | 28.719 | 43.967 | not measured |
| 1,000 | True | go-before | 205.4 (203.7–206.6) | 205,388 | 54.047 | 74.751 | 579.8 |
| 1,000 | True | go | 206.1 (203.8–207.5) | 206,135 | 46.559 | 64.927 | 581.8 |
| 1,000 | True | rust | 238.8 (237.0–240.7) | 238,848 | 47.039 | 77.439 | 557.1 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 33.68 | 84296 | 54.1 | 38.9 | 142.2 | 334.8 | 35.7 |
| go | 31.81 | 84296 | 52.2 | 38.7 | 142.5 | 324.2 | 35.7 |
| rust | 33.21 | 84296 | 37.2 | 42.1 | 126.4 | 254.4 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 443,009 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
