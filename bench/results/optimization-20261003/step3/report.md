# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions; 1.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 6,385 (6,385–6,385) | 2.245 | 4.291 | 6.967 |
| messages_page | 16 | go | 11,002 (11,002–11,002) | 1.338 | 2.515 | 4.263 |
| sidebar | 16 | go | 10,674 (10,674–10,674) | 1.229 | 2.977 | 4.483 |
| search | 16 | go | 11,906 (11,906–11,906) | 1.136 | 2.495 | 4.539 |
| avatar | 16 | go | 40,109 (40,109–40,109) | 0.293 | 0.759 | 1.622 |
| static_css | 16 | go | 298,069 (298,069–298,069) | 0.040 | 0.093 | 0.205 |
| post_message | 16 | go | 3,824 (3,824–3,824) | 2.713 | 9.583 | 16.191 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go | 1,616.0 (1,616.0–1,616.0) | 161,603 | 8.607 | 9.735 | not measured |
| 100 | True | go | 1,844.5 (1,844.5–1,844.5) | 184,455 | 7.791 | 9.279 | 490.4 |
| 1,000 | False | go | 235.6 (235.6–235.6) | 235,640 | 64.479 | 108.863 | not measured |
| 1,000 | True | go | 282.2 (282.2–282.2) | 282,152 | 59.007 | 92.927 | 750.4 |
| 10,000 | False | go | 18.3 (18.3–18.3) | 182,874 | 535.551 | 665.087 | not measured |
| 10,000 | True | go | 22.3 (22.3–22.3) | 223,267 | 346.879 | 556.543 | 592.8 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | 246.30 | 84296 | 33.0 | 42.2 | 136.5 | 955.1 | 37.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

1 completed application runs; 11,063 acknowledged HTTP writes verified in both messages and FTS; 1 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go | 374,036 |
| messages_page | go | 342,444 |
| sidebar | go | 9,462 |
| search | go | 135,497 |
| avatar | go | 3,364 |
| static_css | go | 1,218 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
