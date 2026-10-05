# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 7,377 (6,650–8,056) | 1.564 | 4.411 | 8.727 |
| room_show | 16 | rust | 6,234 (5,998–8,260) | 2.339 | 3.795 | 6.419 |
| messages_page | 16 | go | 8,996 (6,938–9,215) | 1.237 | 3.705 | 8.047 |
| messages_page | 16 | rust | 7,673 (7,505–7,996) | 1.919 | 2.937 | 4.419 |
| sidebar | 16 | go | 14,328 (13,297–15,620) | 0.743 | 2.223 | 6.115 |
| sidebar | 16 | rust | 10,420 (7,682–11,942) | 1.464 | 2.169 | 3.043 |
| search | 16 | go | 10,227 (8,262–11,335) | 1.199 | 2.899 | 6.431 |
| search | 16 | rust | 10,919 (5,983–11,478) | 1.365 | 2.097 | 3.133 |
| post_message | 16 | go | 3,192 (1,438–3,334) | 3.711 | 10.295 | 21.215 |
| post_message | 16 | rust | 3,306 (3,037–3,404) | 4.463 | 6.267 | 13.503 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | 164.34 | 84296 | 79.3 | 39.6 | 135.4 | 180.3 | 37.8 |
| rust | 134.20 | 84296 | 97.9 | 40.7 | 102.1 | 150.5 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

6 completed application runs; 116,548 acknowledged HTTP writes verified in both messages and FTS; 6 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
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

[Interrupted repetitions and resumption](CONTENTION.md): incomplete contention-affected attempts were excluded and retried with unchanged binaries and settings. Completed samples were retained. See that record for host-noise limits.
