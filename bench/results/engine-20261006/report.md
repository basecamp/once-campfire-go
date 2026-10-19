# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 17,120 (17,085–17,229) | 0.723 | 1.816 | 3.587 |
| room_show | 16 | go | 24,519 (24,474–24,556) | 0.502 | 1.257 | 2.585 |
| room_show | 16 | rust | 31,162 (31,030–31,212) | 0.496 | 0.706 | 0.921 |
| messages_page | 16 | go-before | 24,010 (24,006–24,076) | 0.494 | 1.309 | 2.777 |
| messages_page | 16 | go | 25,215 (25,203–25,363) | 0.470 | 1.238 | 2.663 |
| messages_page | 16 | rust | 35,669 (35,409–35,738) | 0.436 | 0.597 | 0.759 |
| sidebar | 16 | go-before | 27,266 (26,524–29,199) | 0.519 | 0.962 | 1.598 |
| sidebar | 16 | go | 26,664 (25,271–27,645) | 0.526 | 1.007 | 1.650 |
| sidebar | 16 | rust | 40,752 (40,651–40,902) | 0.380 | 0.545 | 0.703 |
| search | 16 | go-before | 16,178 (16,176–16,181) | 0.784 | 1.961 | 3.283 |
| search | 16 | go | 26,865 (26,770–26,895) | 0.501 | 1.074 | 1.892 |
| search | 16 | rust | 33,010 (32,646–33,023) | 0.438 | 0.726 | 1.035 |
| post_message | 16 | go-before | 5,376 (5,252–5,420) | 1.780 | 7.059 | 11.751 |
| post_message | 16 | go | 5,399 (5,272–5,442) | 1.784 | 6.951 | 11.799 |
| post_message | 16 | rust | 7,952 (7,824–8,059) | 1.696 | 3.011 | 5.643 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 47.27 | 84296 | 26.8 | 32.9 | 132.1 | 182.8 | 37.9 |
| go | 46.37 | 84296 | 26.4 | 33.1 | 130.8 | 175.1 | 37.9 |
| rust | 50.96 | 84296 | 35.9 | 33.0 | 94.9 | 143.8 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 382,960 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
