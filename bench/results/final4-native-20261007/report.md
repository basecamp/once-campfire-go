# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 27,155 (15,328–28,003) | 0.404 | 1.141 | 3.069 |
| room_show | 16 | go | 48,601 (48,050–48,784) | 0.273 | 0.605 | 1.164 |
| room_show | 16 | rust | 30,987 (30,608–31,232) | 0.497 | 0.709 | 0.938 |
| messages_page | 16 | go-before | 28,079 (15,836–28,694) | 0.380 | 1.121 | 3.107 |
| messages_page | 16 | go | 61,485 (61,473–61,985) | 0.188 | 0.461 | 1.471 |
| messages_page | 16 | rust | 35,605 (35,081–35,804) | 0.436 | 0.597 | 0.759 |
| sidebar | 16 | go-before | 31,923 (17,826–32,910) | 0.396 | 0.963 | 1.817 |
| sidebar | 16 | go | 145,242 (104,377–145,598) | 0.084 | 0.197 | 0.479 |
| sidebar | 16 | rust | 40,593 (39,484–40,696) | 0.381 | 0.549 | 0.711 |
| search | 16 | go-before | 27,994 (13,226–28,179) | 0.462 | 1.063 | 2.069 |
| search | 16 | go | 111,837 (61,684–115,033) | 0.108 | 0.249 | 0.598 |
| search | 16 | rust | 32,616 (32,563–32,664) | 0.443 | 0.736 | 1.046 |
| post_message | 16 | go-before | 5,258 (2,982–5,275) | 1.812 | 7.375 | 12.111 |
| post_message | 16 | go | 9,512 (7,790–9,677) | 1.633 | 2.299 | 11.823 |
| post_message | 16 | rust | 7,723 (7,654–7,844) | 1.728 | 3.083 | 5.799 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,248.6 (1,484.0–2,251.2) | 224,863 | 6.927 | 8.067 | not measured |
| 100 | False | go | 5,548.2 (5,545.3–5,632.4) | 554,819 | 2.281 | 3.189 | not measured |
| 100 | False | rust | 4,249.7 (4,149.2–4,258.6) | 424,974 | 3.457 | 4.855 | not measured |
| 100 | True | go-before | 2,406.0 (2,368.1–2,406.5) | 240,599 | 7.131 | 8.863 | 641.5 |
| 100 | True | go | 3,335.6 (3,289.1–3,345.7) | 333,557 | 3.445 | 5.083 | 889.9 |
| 100 | True | rust | 3,353.1 (3,247.8–3,368.2) | 335,313 | 3.841 | 5.171 | 782.6 |
| 1,000 | False | go-before | 303.5 (302.0–306.3) | 303,461 | 47.583 | 61.823 | not measured |
| 1,000 | False | go | 538.8 (536.0–541.4) | 538,764 | 17.903 | 25.519 | not measured |
| 1,000 | False | rust | 554.8 (544.2–558.9) | 554,821 | 22.319 | 37.855 | not measured |
| 1,000 | True | go-before | 302.3 (301.7–304.8) | 302,259 | 38.175 | 62.463 | 804.2 |
| 1,000 | True | go | 331.7 (324.2–332.7) | 331,748 | 28.271 | 38.303 | 882.5 |
| 1,000 | True | rust | 368.1 (366.8–370.3) | 368,059 | 33.631 | 56.511 | 858.5 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 46.15 | 84296 | 27.0 | 33.0 | 130.4 | 261.2 | 38.1 |
| go | 45.64 | 84296 | 26.7 | 37.6 | 138.9 | 292.3 | 40.2 |
| rust | 50.14 | 84296 | 35.8 | 33.0 | 96.6 | 158.3 | 35.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples (local-only `raw.json`) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. Metadata (local-only `metadata.json`) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 433,170 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
