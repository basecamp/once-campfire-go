# Go with the file and memory SQLite temp stores, and Rust

"go-before" is this pull request built without `PRAGMA temp_store=MEMORY` (SQLite's default file temp store, as in Rust); "go" is this pull request.

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. 2 application workers on CPUs 0-1; load generator on CPUs 2-3. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 17,053 (16,976–17,196) | 0.760 | 1.847 | 3.361 |
| room_show | 16 | go | 17,047 (16,014–17,047) | 0.772 | 1.819 | 3.357 |
| room_show | 16 | rust | 17,253 (17,228–17,425) | 0.901 | 1.180 | 1.516 |
| messages_page | 16 | go-before | 19,348 (19,307–19,445) | 0.673 | 1.569 | 3.033 |
| messages_page | 16 | go | 19,829 (19,540–19,843) | 0.669 | 1.528 | 2.901 |
| messages_page | 16 | rust | 18,784 (18,416–18,839) | 0.824 | 1.057 | 1.462 |
| sidebar | 16 | go-before | 13,671 (13,485–16,261) | 0.970 | 2.349 | 3.939 |
| sidebar | 16 | go | 16,704 (16,570–16,991) | 0.809 | 1.843 | 3.241 |
| sidebar | 16 | rust | 20,643 (20,611–20,720) | 0.757 | 0.997 | 1.257 |
| search | 16 | go-before | 18,674 (18,397–18,810) | 0.726 | 1.609 | 2.993 |
| search | 16 | go | 19,041 (18,958–19,214) | 0.715 | 1.569 | 2.907 |
| search | 16 | rust | 22,630 (22,586–22,943) | 0.675 | 0.939 | 1.295 |
| post_message | 16 | go-before | 6,987 (6,954–7,049) | 1.944 | 4.499 | 7.371 |
| post_message | 16 | go | 6,843 (6,801–7,018) | 1.966 | 4.635 | 7.647 |
| post_message | 16 | rust | 6,608 (6,443–6,749) | 2.251 | 3.263 | 7.679 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 55.32 | 84296 | 52.3 | 40.8 | 137.6 | 181.3 | 36.8 |
| go | 56.16 | 84296 | 52.0 | 40.7 | 134.9 | 178.3 | 36.8 |
| rust | 66.54 | 84296 | 66.4 | 40.9 | 100.3 | 146.2 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples, metadata (source, binary and seed hashes, CPU details, affinity, load averages) and logs were recorded but are not committed.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 421,618 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 29,684 |
| search | go-before | 135,497 |
| room_show | go | 374,036 |
| messages_page | go | 342,444 |
| sidebar | go | 29,684 |
| search | go | 135,497 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
