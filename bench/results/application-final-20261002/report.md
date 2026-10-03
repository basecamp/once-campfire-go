# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 1 | rust | 5,426 (5,236–5,678) | 0.177 | 0.202 | 0.294 |
| room_show | 1 | go | 848 (845–904) | 1.141 | 1.355 | 1.580 |
| room_show | 16 | rust | 27,999 (27,940–28,117) | 0.550 | 0.790 | 1.046 |
| room_show | 16 | go | 2,483 (2,482–2,485) | 5.691 | 11.655 | 18.879 |
| room_show | 64 | rust | 28,178 (28,098–28,291) | 2.211 | 3.143 | 4.051 |
| room_show | 64 | go | 2,498 (2,471–2,506) | 24.559 | 36.863 | 48.927 |
| messages_page | 1 | rust | 7,412 (7,117–7,424) | 0.126 | 0.153 | 0.230 |
| messages_page | 1 | go | 1,944 (1,600–1,980) | 0.442 | 0.695 | 0.826 |
| messages_page | 16 | rust | 31,823 (31,634–32,001) | 0.486 | 0.674 | 0.873 |
| messages_page | 16 | go | 4,470 (4,311–4,509) | 3.323 | 5.911 | 8.991 |
| messages_page | 64 | rust | 31,850 (31,666–31,911) | 1.961 | 2.707 | 3.439 |
| messages_page | 64 | go | 4,515 (4,490–4,535) | 13.375 | 21.535 | 30.191 |
| sidebar | 1 | rust | 9,251 (9,202–9,285) | 0.104 | 0.112 | 0.190 |
| sidebar | 1 | go | 355 (352–356) | 2.717 | 3.089 | 3.317 |
| sidebar | 16 | rust | 38,891 (38,813–39,098) | 0.397 | 0.572 | 0.748 |
| sidebar | 16 | go | 1,425 (1,425–1,428) | 10.183 | 19.807 | 33.279 |
| sidebar | 64 | rust | 39,805 (39,777–40,042) | 1.560 | 2.243 | 2.923 |
| sidebar | 64 | go | 1,427 (1,423–1,431) | 44.095 | 57.567 | 72.127 |
| search | 1 | rust | 9,692 (9,597–9,741) | 0.099 | 0.110 | 0.165 |
| search | 1 | go | 2,245 (2,179–2,250) | 0.416 | 0.559 | 0.695 |
| search | 16 | rust | 30,884 (30,786–30,960) | 0.467 | 0.777 | 1.123 |
| search | 16 | go | 6,652 (6,626–6,700) | 2.099 | 4.463 | 7.543 |
| search | 64 | rust | 35,803 (35,790–35,989) | 1.666 | 2.437 | 3.383 |
| search | 64 | go | 6,839 (6,784–6,876) | 9.007 | 13.383 | 17.951 |
| avatar | 1 | rust | 14,595 (14,594–14,677) | 0.066 | 0.073 | 0.096 |
| avatar | 1 | go | 2,469 (2,467–2,474) | 0.397 | 0.421 | 0.568 |
| avatar | 16 | rust | 57,314 (57,047–57,670) | 0.266 | 0.385 | 0.585 |
| avatar | 16 | go | 10,195 (10,186–10,244) | 1.127 | 2.923 | 5.207 |
| avatar | 64 | rust | 58,810 (58,508–58,932) | 1.042 | 1.522 | 2.121 |
| avatar | 64 | go | 10,184 (10,138–10,194) | 4.579 | 14.343 | 24.911 |
| static_css | 1 | rust | 73,733 (73,476–73,930) | 0.013 | 0.013 | 0.018 |
| static_css | 1 | go | 61,311 (61,235–61,674) | 0.015 | 0.017 | 0.032 |
| static_css | 16 | rust | 502,211 (498,825–502,917) | 0.031 | 0.042 | 0.054 |
| static_css | 16 | go | 302,343 (301,824–304,073) | 0.039 | 0.092 | 0.197 |
| static_css | 64 | rust | 527,256 (526,952–527,397) | 0.117 | 0.178 | 0.240 |
| static_css | 64 | go | 307,898 (307,873–308,514) | 0.141 | 0.436 | 1.102 |
| post_message | 1 | rust | 3,692 (3,692–3,706) | 0.227 | 0.283 | 1.924 |
| post_message | 1 | go | 1,001 (983–1,008) | 0.909 | 0.998 | 4.647 |
| post_message | 16 | rust | 7,889 (7,823–7,930) | 1.720 | 2.937 | 5.987 |
| post_message | 16 | go | 3,180 (3,172–3,182) | 3.773 | 10.279 | 18.351 |
| post_message | 64 | rust | 7,973 (7,911–8,008) | 7.699 | 10.071 | 12.607 |
| post_message | 64 | go | 3,148 (3,120–3,149) | 14.959 | 44.543 | 87.551 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | rust | 4,111.0 (4,094.0–4,199.7) | 411,103 | 3.113 | 3.733 | not measured |
| 100 | False | go | 575.9 (567.1–579.3) | 57,586 | 12.743 | 15.143 | not measured |
| 100 | True | rust | 3,433.7 (3,385.3–3,457.5) | 343,374 | 3.745 | 5.239 | 801.3 |
| 100 | True | go | 454.8 (433.8–460.3) | 45,482 | 19.631 | 25.615 | 120.1 |
| 1,000 | False | rust | 558.4 (554.9–569.0) | 558,365 | 21.135 | 35.839 | not measured |
| 1,000 | False | go | 54.2 (53.6–54.5) | 54,157 | 71.167 | 88.895 | not measured |
| 1,000 | True | rust | 367.9 (367.8–376.1) | 367,872 | 32.159 | 51.679 | 857.8 |
| 1,000 | True | go | 44.2 (44.1–44.3) | 44,208 | 93.951 | 118.783 | 116.6 |
| 10,000 | False | rust | 69.9 (69.0–70.7) | 699,346 | 338.943 | 765.951 | not measured |
| 10,000 | False | go | 4.1 (4.1–4.2) | 41,228 | 687.615 | 756.735 | not measured |
| 10,000 | True | rust | 39.0 (38.5–39.3) | 389,599 | 525.311 | 604.159 | 908.1 |
| 10,000 | True | go | 3.5 (3.5–3.5) | 34,954 | 859.647 | 916.479 | 92.3 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| rust | 30.61 | 84296 | 36.5 | 40.8 | 108.5 | 417.0 | 35.8 |
| go | 28.99 | 84296 | 203.1 | 41.3 | 142.8 | 1018.8 | 37.7 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

6 completed application runs; 465,722 acknowledged HTTP writes verified in both messages and FTS; 30 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
