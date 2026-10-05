# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

6 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 1,409 (1,376–1,462) | 9.423 | 20.359 | 36.143 |
| room_show | 16 | go | 7,982 (7,778–8,642) | 1.734 | 3.593 | 5.667 |
| room_show | 16 | rust | 11,208 (10,769–11,967) | 1.373 | 1.982 | 2.631 |
| messages_page | 16 | go-before | 1,926 (1,776–2,054) | 7.057 | 15.351 | 25.895 |
| messages_page | 16 | go | 8,844 (8,614–8,911) | 1.528 | 3.369 | 5.483 |
| messages_page | 16 | rust | 12,598 (12,333–13,161) | 1.229 | 1.720 | 2.224 |
| sidebar | 16 | go-before | 5,463 (5,256–5,695) | 2.636 | 4.887 | 8.113 |
| sidebar | 16 | go | 7,089 (6,598–7,514) | 2.100 | 3.367 | 5.049 |
| sidebar | 16 | rust | 11,271 (10,825–11,811) | 1.363 | 2.003 | 2.716 |
| search | 16 | go-before | 2,447 (2,311–2,595) | 5.253 | 13.103 | 20.695 |
| search | 16 | go | 4,980 (4,509–5,053) | 2.803 | 5.691 | 9.175 |
| search | 16 | rust | 10,000 (9,446–10,544) | 1.488 | 2.288 | 3.372 |
| post_message | 16 | go-before | 2,146 (2,070–2,222) | 5.439 | 15.895 | 28.951 |
| post_message | 16 | go | 2,145 (2,109–2,175) | 5.535 | 15.527 | 28.023 |
| post_message | 16 | rust | 2,178 (2,140–2,242) | 5.667 | 7.411 | 100.383 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 124.92 | 84296 | 53.1 | 27.3 | 126.7 | 168.6 | 37.7 |
| go | 121.56 | 84296 | 53.1 | 27.8 | 127.8 | 169.8 | 37.7 |
| rust | 128.26 | 84296 | 80.6 | 32.6 | 107.2 | 148.7 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

18 completed application runs; 269,767 acknowledged HTTP writes verified in both messages and FTS; 18 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
