# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Fly.io performance-16x Machine background load recorded.

6 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 4,313 (4,194–4,553) | 3.316 | 6.341 | 9.667 |
| room_show | 16 | go | 5,428 (5,189–5,556) | 2.562 | 5.187 | 8.123 |
| room_show | 16 | rust | 6,437 (6,164–6,826) | 2.390 | 3.392 | 4.409 |
| messages_page | 16 | go-before | 5,984 (5,771–6,496) | 2.290 | 4.749 | 7.591 |
| messages_page | 16 | go | 5,937 (5,845–6,083) | 2.290 | 4.799 | 7.851 |
| messages_page | 16 | rust | 6,851 (6,596–7,210) | 2.254 | 3.118 | 4.012 |
| sidebar | 16 | go-before | 4,585 (4,255–5,408) | 3.309 | 4.993 | 6.927 |
| sidebar | 16 | go | 5,129 (4,601–5,808) | 2.936 | 4.501 | 6.407 |
| sidebar | 16 | rust | 8,190 (7,949–8,670) | 1.884 | 2.716 | 3.546 |
| search | 16 | go-before | 4,018 (3,662–4,158) | 3.634 | 6.573 | 9.903 |
| search | 16 | go | 4,087 (3,872–4,393) | 3.555 | 6.449 | 9.855 |
| search | 16 | rust | 7,351 (7,137–7,749) | 2.025 | 3.093 | 4.525 |
| post_message | 16 | go-before | 1,945 (1,922–2,028) | 5.995 | 17.319 | 31.175 |
| post_message | 16 | go | 1,934 (1,894–1,989) | 6.029 | 17.359 | 30.983 |
| post_message | 16 | rust | 2,090 (2,066–2,138) | 7.471 | 8.875 | 13.263 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 114.66 | 84296 | 52.9 | 27.3 | 119.2 | 161.6 | 37.7 |
| go | 113.71 | 84296 | 53.0 | 27.7 | 118.8 | 160.0 | 37.7 |
| rust | 119.10 | 84296 | 80.8 | 32.5 | 87.6 | 131.1 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are Fly.io performance-16x Machine measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

18 completed application runs; 246,052 acknowledged HTTP writes verified in both messages and FTS; 18 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
