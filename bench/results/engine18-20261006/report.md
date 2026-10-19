# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 24,262 (24,242–24,311) | 0.506 | 1.262 | 2.631 |
| room_show | 16 | go | 27,985 (27,930–28,220) | 0.389 | 1.101 | 3.031 |
| room_show | 16 | rust | 30,842 (30,818–30,883) | 0.500 | 0.715 | 0.945 |
| messages_page | 16 | go-before | 25,065 (25,064–25,311) | 0.475 | 1.252 | 2.657 |
| messages_page | 16 | go | 28,633 (28,619–28,751) | 0.372 | 1.087 | 3.111 |
| messages_page | 16 | rust | 35,527 (35,026–35,539) | 0.437 | 0.599 | 0.760 |
| sidebar | 16 | go-before | 26,072 (25,996–27,627) | 0.539 | 1.018 | 1.671 |
| sidebar | 16 | go | 34,161 (31,422–34,324) | 0.360 | 0.909 | 1.770 |
| sidebar | 16 | rust | 40,557 (40,433–40,595) | 0.382 | 0.548 | 0.709 |
| search | 16 | go-before | 26,766 (26,713–26,784) | 0.502 | 1.070 | 1.903 |
| search | 16 | go | 27,496 (27,259–27,598) | 0.469 | 1.096 | 2.047 |
| search | 16 | rust | 32,711 (32,569–32,840) | 0.440 | 0.735 | 1.044 |
| post_message | 16 | go-before | 5,271 (5,077–5,386) | 1.792 | 7.179 | 12.015 |
| post_message | 16 | go | 5,271 (5,254–5,279) | 1.797 | 7.279 | 12.023 |
| post_message | 16 | rust | 7,909 (7,870–7,923) | 1.714 | 3.013 | 5.483 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 46.86 | 84296 | 26.4 | 31.0 | 130.4 | 175.5 | 37.9 |
| go | 46.25 | 84296 | 26.3 | 34.1 | 132.0 | 178.8 | 38.1 |
| rust | 50.31 | 84296 | 36.0 | 33.0 | 94.1 | 142.5 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 380,597 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
