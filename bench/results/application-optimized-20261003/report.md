# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 1 | rust | 6,111 (5,617–6,250) | 0.153 | 0.187 | 0.266 |
| room_show | 1 | go | 3,017 (2,989–3,097) | 0.310 | 0.369 | 0.587 |
| room_show | 16 | rust | 27,900 (27,814–28,131) | 0.553 | 0.794 | 1.047 |
| room_show | 16 | go | 10,629 (10,624–10,683) | 1.204 | 2.887 | 5.539 |
| room_show | 64 | rust | 27,986 (27,982–28,007) | 2.223 | 3.157 | 4.077 |
| room_show | 64 | go | 10,628 (10,201–10,675) | 5.815 | 8.759 | 11.879 |
| messages_page | 1 | rust | 7,227 (7,144–7,353) | 0.127 | 0.164 | 0.234 |
| messages_page | 1 | go | 4,940 (4,205–5,472) | 0.184 | 0.205 | 0.343 |
| messages_page | 16 | rust | 31,622 (31,524–31,891) | 0.490 | 0.676 | 0.875 |
| messages_page | 16 | go | 21,582 (20,483–21,734) | 0.538 | 1.465 | 3.239 |
| messages_page | 64 | rust | 31,696 (31,398–31,970) | 1.969 | 2.713 | 3.447 |
| messages_page | 64 | go | 21,476 (19,963–21,548) | 2.863 | 4.627 | 7.047 |
| sidebar | 1 | rust | 9,122 (9,072–9,319) | 0.105 | 0.113 | 0.190 |
| sidebar | 1 | go | 7,436 (6,567–7,994) | 0.129 | 0.151 | 0.275 |
| sidebar | 16 | rust | 38,578 (38,238–38,902) | 0.400 | 0.577 | 0.754 |
| sidebar | 16 | go | 26,531 (23,035–27,145) | 0.532 | 0.996 | 1.653 |
| sidebar | 64 | rust | 39,436 (39,252–39,811) | 1.571 | 2.273 | 2.969 |
| sidebar | 64 | go | 26,508 (21,586–27,364) | 2.351 | 3.473 | 4.499 |
| search | 1 | rust | 9,683 (9,487–9,753) | 0.099 | 0.110 | 0.165 |
| search | 1 | go | 4,472 (4,451–4,502) | 0.212 | 0.243 | 0.453 |
| search | 16 | rust | 30,698 (30,682–30,945) | 0.471 | 0.779 | 1.126 |
| search | 16 | go | 15,285 (15,216–15,390) | 0.835 | 2.065 | 3.419 |
| search | 64 | rust | 35,574 (35,523–36,028) | 1.682 | 2.435 | 3.377 |
| search | 64 | go | 15,188 (15,173–15,228) | 4.059 | 6.103 | 8.479 |
| avatar | 1 | rust | 14,617 (14,572–14,951) | 0.066 | 0.073 | 0.097 |
| avatar | 1 | go | 9,932 (9,624–10,027) | 0.095 | 0.109 | 0.195 |
| avatar | 16 | rust | 57,148 (56,622–57,310) | 0.267 | 0.386 | 0.589 |
| avatar | 16 | go | 40,710 (39,389–40,777) | 0.288 | 0.743 | 1.654 |
| avatar | 64 | rust | 58,738 (58,432–58,784) | 1.047 | 1.520 | 2.083 |
| avatar | 64 | go | 41,144 (40,657–41,159) | 1.433 | 2.497 | 3.689 |
| static_css | 1 | rust | 73,106 (72,489–74,343) | 0.013 | 0.013 | 0.018 |
| static_css | 1 | go | 61,701 (61,521–61,758) | 0.015 | 0.017 | 0.029 |
| static_css | 16 | rust | 498,442 (496,427–500,417) | 0.031 | 0.042 | 0.055 |
| static_css | 16 | go | 314,163 (312,347–314,167) | 0.039 | 0.089 | 0.182 |
| static_css | 64 | rust | 522,065 (520,713–523,848) | 0.118 | 0.180 | 0.249 |
| static_css | 64 | go | 327,382 (325,094–328,999) | 0.139 | 0.406 | 0.943 |
| post_message | 1 | rust | 3,705 (3,680–3,720) | 0.226 | 0.283 | 1.867 |
| post_message | 1 | go | 1,599 (1,590–1,601) | 0.533 | 0.606 | 4.211 |
| post_message | 16 | rust | 7,849 (7,816–7,938) | 1.721 | 2.957 | 5.867 |
| post_message | 16 | go | 4,068 (4,052–4,079) | 2.545 | 8.943 | 15.391 |
| post_message | 64 | rust | 8,004 (8,000–8,040) | 7.651 | 9.967 | 12.183 |
| post_message | 64 | go | 3,985 (3,937–3,998) | 11.991 | 35.487 | 70.911 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | rust | 4,105.4 (4,102.8–4,145.3) | 410,537 | 3.147 | 3.999 | not measured |
| 100 | False | go | 1,665.2 (1,652.5–1,675.8) | 166,520 | 8.007 | 9.383 | not measured |
| 100 | True | rust | 3,461.7 (3,451.7–3,462.1) | 346,168 | 3.719 | 5.283 | 807.9 |
| 100 | True | go | 1,877.9 (1,856.0–1,933.9) | 187,787 | 7.875 | 10.215 | 500.3 |
| 1,000 | False | rust | 566.6 (547.9–567.2) | 566,624 | 18.495 | 31.583 | not measured |
| 1,000 | False | go | 236.7 (234.3–239.3) | 236,721 | 75.199 | 130.111 | not measured |
| 1,000 | True | rust | 373.3 (370.1–378.5) | 373,315 | 28.655 | 45.311 | 870.7 |
| 1,000 | True | go | 289.6 (289.2–290.3) | 289,559 | 65.343 | 111.295 | 770.2 |
| 10,000 | False | rust | 67.8 (61.1–73.5) | 677,586 | 549.375 | 1163.263 | not measured |
| 10,000 | False | go | 17.8 (17.7–17.9) | 177,963 | 393.727 | 710.655 | not measured |
| 10,000 | True | rust | 39.3 (37.7–40.1) | 393,352 | 482.815 | 555.007 | 917.0 |
| 10,000 | True | go | 21.1 (21.1–29.3) | 211,431 | 378.111 | 627.711 | 561.5 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| rust | 30.05 | 84296 | 40.9 | 40.7 | 108.0 | 408.3 | 35.8 |
| go | 30.30 | 84296 | 30.4 | 40.8 | 145.9 | 1023.1 | 37.8 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

6 completed application runs; 506,897 acknowledged HTTP writes verified in both messages and FTS; 30 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |
| avatar | rust | 3,364 |
| static_css | rust | 1,218 |
| room_show | go | 374,036 |
| messages_page | go | 342,444 |
| sidebar | go | 9,462 |
| search | go | 135,497 |
| avatar | go | 3,364 |
| static_css | go | 1,218 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.

[Harness hashes and build commands](harness.json). Native binary sizes reflect these build flags; the production Go image strips symbols. Server/load-generator logs are retained alongside the raw JSON (gzip-compressed after measurement when archived).

[Interrupted repetitions and resumption](CONTENTION.md): incomplete contention-affected attempts were excluded and retried with unchanged binaries and settings. Completed samples were retained. See that record for host-noise limits.
