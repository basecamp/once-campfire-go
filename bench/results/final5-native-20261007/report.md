# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 28,054 (27,872–28,086) | 0.389 | 1.099 | 3.045 |
| room_show | 16 | go | 48,416 (48,198–48,983) | 0.272 | 0.606 | 1.195 |
| room_show | 16 | rust | 31,147 (31,093–31,159) | 0.495 | 0.707 | 0.927 |
| messages_page | 16 | go-before | 28,704 (28,306–28,714) | 0.373 | 1.095 | 3.069 |
| messages_page | 16 | go | 61,356 (60,943–61,808) | 0.191 | 0.459 | 1.411 |
| messages_page | 16 | rust | 35,714 (35,662–35,732) | 0.435 | 0.594 | 0.759 |
| sidebar | 16 | go-before | 30,621 (29,712–32,800) | 0.415 | 0.999 | 1.809 |
| sidebar | 16 | go | 145,288 (145,216–146,162) | 0.084 | 0.198 | 0.471 |
| sidebar | 16 | rust | 40,914 (40,889–41,147) | 0.378 | 0.543 | 0.701 |
| search | 16 | go-before | 27,737 (27,658–27,925) | 0.465 | 1.074 | 1.979 |
| search | 16 | go | 113,797 (112,909–114,126) | 0.107 | 0.245 | 0.562 |
| search | 16 | rust | 32,755 (32,673–33,018) | 0.441 | 0.730 | 1.040 |
| post_message | 16 | go-before | 5,198 (5,188–5,324) | 1.798 | 7.323 | 12.031 |
| post_message | 16 | go | 9,852 (9,806–9,886) | 1.623 | 2.285 | 9.367 |
| post_message | 16 | rust | 7,859 (7,847–7,894) | 1.702 | 3.077 | 5.547 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,257.3 (2,230.7–2,259.6) | 225,726 | 6.951 | 8.115 | not measured |
| 100 | False | go | 5,434.5 (5,423.7–5,505.7) | 543,447 | 2.351 | 3.275 | not measured |
| 100 | False | rust | 4,200.8 (4,158.2–4,246.5) | 420,081 | 3.371 | 4.771 | not measured |
| 100 | True | go-before | 2,396.8 (2,389.8–2,463.1) | 239,680 | 6.747 | 8.727 | 639.0 |
| 100 | True | go | 3,316.9 (3,279.5–3,335.2) | 331,690 | 3.383 | 4.927 | 884.9 |
| 100 | True | rust | 3,285.5 (3,280.1–3,367.7) | 328,550 | 3.849 | 5.323 | 766.8 |
| 1,000 | False | go-before | 308.0 (303.7–316.9) | 307,964 | 55.263 | 96.063 | not measured |
| 1,000 | False | go | 540.2 (525.6–542.0) | 540,228 | 16.911 | 23.999 | not measured |
| 1,000 | False | rust | 537.3 (535.6–553.0) | 537,253 | 21.359 | 32.895 | not measured |
| 1,000 | True | go-before | 300.2 (300.2–301.7) | 300,200 | 40.703 | 57.727 | 799.0 |
| 1,000 | True | go | 331.0 (330.9–332.1) | 330,974 | 27.663 | 38.783 | 880.5 |
| 1,000 | True | rust | 363.9 (362.8–367.7) | 363,908 | 34.911 | 71.487 | 848.7 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 47.27 | 84296 | 26.8 | 33.0 | 131.5 | 260.4 | 38.1 |
| go | 44.94 | 84296 | 26.5 | 37.7 | 139.9 | 299.1 | 40.2 |
| rust | 50.28 | 84296 | 35.6 | 33.0 | 94.4 | 154.5 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 466,535 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
