# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 16,895 (15,729–17,113) | 0.739 | 1.852 | 3.581 |
| room_show | 16 | rust | 30,455 (18,419–30,825) | 0.503 | 0.726 | 0.999 |
| messages_page | 16 | go | 23,828 (21,658–23,842) | 0.500 | 1.315 | 2.853 |
| messages_page | 16 | rust | 34,637 (13,789–35,086) | 0.442 | 0.614 | 0.918 |
| sidebar | 16 | go | 25,804 (24,407–28,912) | 0.527 | 1.035 | 1.952 |
| sidebar | 16 | rust | 39,420 (24,640–40,270) | 0.389 | 0.566 | 0.780 |
| search | 16 | go | 16,001 (15,905–16,033) | 0.792 | 2.008 | 3.317 |
| search | 16 | rust | 31,631 (29,378–32,537) | 0.456 | 0.759 | 1.099 |
| post_message | 16 | go | 4,753 (2,044–5,100) | 1.898 | 7.739 | 16.311 |
| post_message | 16 | rust | 6,760 (6,595–7,761) | 1.742 | 3.083 | 7.711 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | 47.49 | 84296 | 26.9 | 35.2 | 129.1 | 176.8 | 37.9 |
| rust | 51.50 | 84296 | 36.5 | 33.2 | 97.4 | 146.5 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

6 completed application runs; 227,621 acknowledged HTTP writes verified in both messages and FTS; 6 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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
