# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 27,936 (27,806–28,053) | 0.389 | 1.105 | 3.051 |
| room_show | 16 | go | 47,916 (47,808–47,925) | 0.243 | 0.603 | 1.994 |
| room_show | 16 | rust | 30,813 (30,691–30,966) | 0.500 | 0.713 | 0.940 |
| messages_page | 16 | go-before | 28,640 (28,595–28,830) | 0.373 | 1.087 | 3.049 |
| messages_page | 16 | go | 54,976 (54,910–55,661) | 0.210 | 0.518 | 1.792 |
| messages_page | 16 | rust | 35,424 (35,358–35,616) | 0.437 | 0.600 | 0.760 |
| sidebar | 16 | go-before | 32,044 (31,764–34,100) | 0.391 | 0.955 | 1.788 |
| sidebar | 16 | go | 129,331 (129,210–129,579) | 0.096 | 0.220 | 0.561 |
| sidebar | 16 | rust | 40,227 (39,927–40,865) | 0.385 | 0.553 | 0.713 |
| search | 16 | go-before | 27,704 (27,595–27,949) | 0.466 | 1.079 | 1.996 |
| search | 16 | go | 92,451 (90,932–92,872) | 0.132 | 0.306 | 0.762 |
| search | 16 | rust | 32,702 (32,404–32,732) | 0.440 | 0.737 | 1.046 |
| post_message | 16 | go-before | 5,266 (5,216–5,430) | 1.800 | 7.263 | 11.863 |
| post_message | 16 | go | 6,282 (6,210–6,302) | 2.283 | 3.715 | 9.575 |
| post_message | 16 | rust | 7,842 (7,777–7,871) | 1.711 | 3.061 | 5.775 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,256.7 (2,246.6–2,257.6) | 225,673 | 6.767 | 8.043 | not measured |
| 100 | False | go | 3,769.9 (3,689.4–3,821.8) | 376,989 | 3.011 | 3.559 | not measured |
| 100 | False | rust | 4,231.2 (3,191.8–4,233.4) | 423,121 | 3.433 | 4.663 | not measured |
| 100 | True | go-before | 2,410.9 (2,401.0–2,429.9) | 241,086 | 6.759 | 8.423 | 642.6 |
| 100 | True | go | 3,105.1 (3,042.2–3,157.7) | 310,506 | 3.533 | 4.687 | 828.1 |
| 100 | True | rust | 3,338.5 (3,325.2–3,342.0) | 333,849 | 3.917 | 5.431 | 779.0 |
| 1,000 | False | go-before | 309.5 (300.9–311.1) | 309,523 | 48.511 | 93.055 | not measured |
| 1,000 | False | go | 492.3 (488.6–497.5) | 492,272 | 22.623 | 32.143 | not measured |
| 1,000 | False | rust | 550.6 (546.5–552.7) | 550,627 | 21.295 | 35.135 | not measured |
| 1,000 | True | go-before | 300.4 (297.1–305.1) | 300,361 | 43.903 | 70.271 | 799.1 |
| 1,000 | True | go | 322.8 (322.8–326.2) | 322,812 | 28.591 | 39.807 | 858.5 |
| 1,000 | True | rust | 364.1 (362.3–365.5) | 364,080 | 31.967 | 57.695 | 849.1 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 46.31 | 84296 | 26.6 | 33.1 | 133.2 | 260.7 | 38.1 |
| go | 47.01 | 84296 | 26.7 | 37.9 | 136.2 | 289.9 | 40.0 |
| rust | 49.44 | 84296 | 35.8 | 32.9 | 98.5 | 156.2 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 394,573 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
