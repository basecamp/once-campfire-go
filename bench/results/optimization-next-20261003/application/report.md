# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 10,175 (10,151–10,370) | 1.260 | 2.995 | 5.691 |
| room_show | 16 | go | 15,218 (15,194–15,330) | 0.810 | 2.071 | 4.143 |
| room_show | 16 | rust | 27,535 (27,111–27,719) | 0.558 | 0.807 | 1.089 |
| messages_page | 16 | go-before | 21,445 (21,208–21,658) | 0.552 | 1.474 | 3.059 |
| messages_page | 16 | go | 21,369 (21,080–21,401) | 0.551 | 1.494 | 3.181 |
| messages_page | 16 | rust | 31,139 (31,046–31,237) | 0.496 | 0.689 | 0.911 |
| sidebar | 16 | go-before | 24,423 (23,110–25,002) | 0.572 | 1.109 | 1.887 |
| sidebar | 16 | go | 24,658 (24,582–26,121) | 0.574 | 1.050 | 1.764 |
| sidebar | 16 | rust | 38,303 (38,060–38,593) | 0.403 | 0.581 | 0.758 |
| search | 16 | go-before | 14,817 (14,562–14,906) | 0.861 | 2.131 | 3.545 |
| search | 16 | go | 14,841 (13,959–14,862) | 0.855 | 2.149 | 3.631 |
| search | 16 | rust | 30,807 (30,198–30,940) | 0.470 | 0.777 | 1.133 |
| post_message | 16 | go-before | 4,025 (3,991–4,037) | 2.589 | 8.927 | 16.031 |
| post_message | 16 | go | 5,036 (4,999–5,075) | 1.870 | 7.539 | 12.487 |
| post_message | 16 | rust | 7,740 (7,168–7,824) | 1.754 | 2.943 | 6.003 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 51.52 | 84296 | 30.5 | 36.6 | 136.6 | 186.3 | 37.8 |
| go | 50.97 | 84296 | 26.3 | 37.0 | 137.3 | 185.8 | 37.9 |
| rust | 57.88 | 84296 | 35.9 | 40.2 | 104.0 | 157.1 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 345,913 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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

[Harness hashes and build commands](harness.json). Native binary sizes reflect these build flags; the production Go image strips symbols. Server/load-generator logs are retained alongside the raw JSON (gzip-compressed after measurement when archived).
