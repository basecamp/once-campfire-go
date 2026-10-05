# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Fly.io performance-16x Machine background load recorded.

6 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 1,282 (1,260–1,290) | 9.687 | 23.231 | 42.015 |
| room_show | 16 | go | 6,678 (6,493–6,906) | 2.130 | 4.052 | 6.413 |
| room_show | 16 | rust | 8,424 (8,200–9,541) | 1.834 | 2.610 | 3.402 |
| messages_page | 16 | go-before | 1,646 (1,624–1,675) | 7.925 | 18.031 | 29.375 |
| messages_page | 16 | go | 7,277 (6,946–7,489) | 1.923 | 3.857 | 6.059 |
| messages_page | 16 | rust | 8,974 (8,606–9,492) | 1.732 | 2.393 | 3.053 |
| sidebar | 16 | go-before | 4,486 (4,152–4,625) | 3.321 | 5.385 | 8.559 |
| sidebar | 16 | go | 5,148 (4,951–5,638) | 2.945 | 4.455 | 6.237 |
| sidebar | 16 | rust | 8,284 (7,716–8,414) | 1.860 | 2.691 | 3.517 |
| search | 16 | go-before | 2,233 (2,155–2,300) | 5.799 | 14.339 | 22.479 |
| search | 16 | go | 4,148 (4,098–4,270) | 3.527 | 6.247 | 9.387 |
| search | 16 | rust | 8,037 (7,691–8,328) | 1.865 | 2.825 | 4.151 |
| post_message | 16 | go-before | 1,874 (1,824–1,926) | 6.325 | 17.719 | 32.199 |
| post_message | 16 | go | 1,906 (1,868–1,934) | 6.183 | 17.527 | 31.895 |
| post_message | 16 | rust | 1,969 (1,963–2,092) | 7.935 | 9.591 | 14.191 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 117.94 | 84296 | 52.7 | 27.2 | 126.4 | 167.2 | 37.7 |
| go | 115.01 | 84296 | 52.6 | 27.7 | 127.9 | 168.6 | 37.7 |
| rust | 118.41 | 84296 | 81.5 | 32.5 | 105.2 | 151.9 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are Fly.io performance-16x Machine measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. The application listeners gzip these responses themselves, and that cost is measured. TLS, ACME, zstd, front-server compression and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

18 completed application runs; 236,081 acknowledged HTTP writes verified in both messages and FTS; 18 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
