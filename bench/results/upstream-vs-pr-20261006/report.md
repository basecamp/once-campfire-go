# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 12,370 (12,137–12,468) | 1.089 | 2.379 | 4.167 |
| room_show | 16 | go | 20,070 (19,649–20,447) | 0.670 | 1.455 | 2.541 |
| room_show | 16 | rust | 21,383 (21,374–21,392) | 0.713 | 0.998 | 1.368 |
| messages_page | 16 | go-before | 17,588 (15,561–17,768) | 0.742 | 1.687 | 3.051 |
| messages_page | 16 | go | 21,488 (21,268–21,809) | 0.624 | 1.371 | 2.343 |
| messages_page | 16 | rust | 22,803 (22,699–22,998) | 0.671 | 0.913 | 1.231 |
| sidebar | 16 | go-before | 15,719 (13,528–16,143) | 0.919 | 1.607 | 2.673 |
| sidebar | 16 | go | 22,189 (20,692–22,875) | 0.587 | 1.445 | 2.371 |
| sidebar | 16 | rust | 27,271 (26,768–27,364) | 0.568 | 0.800 | 1.057 |
| search | 16 | go-before | 12,820 (11,758–12,949) | 1.065 | 2.321 | 3.823 |
| search | 16 | go | 24,590 (24,344–24,728) | 0.544 | 1.236 | 2.030 |
| search | 16 | rust | 27,079 (26,821–27,237) | 0.548 | 0.842 | 1.235 |
| avatar | 16 | go-before | 27,936 (27,180–28,033) | 0.476 | 1.033 | 2.051 |
| avatar | 16 | go | 35,436 (34,383–35,577) | 0.372 | 0.863 | 1.513 |
| avatar | 16 | rust | 36,147 (36,033–36,270) | 0.423 | 0.606 | 0.887 |
| static_css | 16 | go-before | 193,401 (192,265–196,512) | 0.062 | 0.139 | 0.341 |
| static_css | 16 | go | 204,196 (202,705–206,289) | 0.061 | 0.129 | 0.324 |
| static_css | 16 | rust | 255,880 (251,337–257,343) | 0.059 | 0.090 | 0.126 |
| post_message | 16 | go-before | 4,833 (4,641–4,895) | 1.835 | 8.255 | 12.935 |
| post_message | 16 | go | 7,731 (7,630–7,742) | 1.904 | 3.193 | 6.471 |
| post_message | 16 | rust | 7,451 (7,392–7,503) | 1.940 | 2.871 | 7.619 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 1,822.8 (1,764.8–1,834.4) | 182,275 | 10.439 | 14.103 | not measured |
| 100 | False | go | 4,173.9 (3,939.1–4,246.0) | 417,387 | 4.959 | 6.815 | not measured |
| 100 | False | rust | 3,859.0 (3,838.6–3,879.0) | 385,899 | 3.219 | 5.079 | not measured |
| 100 | True | go-before | 1,745.5 (1,721.3–1,766.4) | 174,546 | 9.431 | 13.847 | 465.0 |
| 100 | True | go | 2,042.3 (2,016.5–2,048.3) | 204,235 | 6.211 | 9.159 | 576.7 |
| 100 | True | rust | 2,298.0 (2,293.3–2,304.5) | 229,803 | 4.703 | 6.903 | 536.3 |
| 1,000 | False | go-before | 269.9 (268.5–270.0) | 269,897 | 89.855 | 170.367 | not measured |
| 1,000 | False | go | 505.8 (497.3–509.7) | 505,838 | 25.935 | 38.143 | not measured |
| 1,000 | False | rust | 538.3 (536.8–541.6) | 538,342 | 28.735 | 43.263 | not measured |
| 1,000 | True | go-before | 210.8 (210.7–210.9) | 210,797 | 48.735 | 72.895 | 560.9 |
| 1,000 | True | go | 208.4 (207.6–208.9) | 208,421 | 52.479 | 80.255 | 588.3 |
| 1,000 | True | rust | 239.7 (238.5–240.5) | 239,661 | 46.431 | 72.639 | 558.9 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 30.57 | 84296 | 52.2 | 40.3 | 153.8 | 357.4 | 36.2 |
| go | 32.49 | 84296 | 52.0 | 38.9 | 180.8 | 400.3 | 36.4 |
| rust | 33.52 | 84296 | 37.5 | 42.1 | 128.3 | 256.4 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 409,946 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 9,462 |
| search | go-before | 135,497 |
| avatar | go-before | 3,364 |
| static_css | go-before | 1,218 |
| room_show | go | 416,139 |
| messages_page | go | 383,844 |
| sidebar | go | 30,763 |
| search | go | 149,625 |
| avatar | go | 3,364 |
| static_css | go | 1,218 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |
| avatar | rust | 3,364 |
| static_css | rust | 1,218 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
