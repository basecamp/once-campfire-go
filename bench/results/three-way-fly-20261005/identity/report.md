# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

6 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 5,506 (5,327–5,713) | 2.532 | 5.171 | 8.063 |
| room_show | 16 | go | 6,932 (6,338–7,272) | 1.951 | 4.175 | 6.767 |
| room_show | 16 | rust | 9,184 (8,272–9,459) | 1.661 | 2.386 | 3.218 |
| messages_page | 16 | go-before | 7,629 (7,332–7,841) | 1.743 | 3.845 | 6.265 |
| messages_page | 16 | go | 7,753 (7,285–7,981) | 1.719 | 3.789 | 6.245 |
| messages_page | 16 | rust | 10,055 (9,392–10,653) | 1.526 | 2.135 | 2.791 |
| sidebar | 16 | go-before | 6,191 (5,716–7,400) | 2.414 | 3.843 | 5.645 |
| sidebar | 16 | go | 7,004 (6,558–7,427) | 2.131 | 3.354 | 5.041 |
| sidebar | 16 | rust | 11,441 (10,661–12,003) | 1.344 | 1.965 | 2.638 |
| search | 16 | go-before | 4,990 (4,613–5,192) | 2.774 | 5.765 | 9.207 |
| search | 16 | go | 5,138 (4,950–5,303) | 2.690 | 5.611 | 8.887 |
| search | 16 | rust | 9,807 (9,358–10,284) | 1.510 | 2.332 | 3.447 |
| post_message | 16 | go-before | 2,034 (1,946–2,115) | 4.961 | 18.543 | 31.431 |
| post_message | 16 | go | 2,012 (1,948–2,101) | 4.729 | 19.895 | 33.055 |
| post_message | 16 | rust | 2,007 (1,897–2,046) | 4.855 | 6.247 | 133.183 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 139.99 | 84296 | 53.1 | 27.3 | 120.2 | 161.6 | 37.7 |
| go | 128.12 | 84296 | 53.5 | 27.9 | 119.6 | 162.9 | 37.7 |
| rust | 127.13 | 84296 | 79.1 | 32.5 | 88.3 | 132.2 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

18 completed application runs; 271,510 acknowledged HTTP writes verified in both messages and FTS; 18 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
