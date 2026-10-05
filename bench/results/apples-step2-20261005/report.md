# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-2; load generator on CPUs 3-5. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 13,211 (12,428–13,485) | 0.942 | 2.469 | 3.931 |
| room_show | 16 | go | 11,739 (11,618–11,870) | 1.139 | 2.555 | 4.443 |
| room_show | 16 | rust | 21,383 (21,251–21,548) | 0.713 | 1.001 | 1.364 |
| messages_page | 16 | go-before | 18,546 (17,107–18,776) | 0.643 | 1.838 | 3.299 |
| messages_page | 16 | go | 15,255 (15,135–15,638) | 0.861 | 2.014 | 3.329 |
| messages_page | 16 | rust | 22,880 (22,800–23,085) | 0.671 | 0.910 | 1.211 |
| sidebar | 16 | go-before | 21,757 (20,758–21,984) | 0.592 | 1.477 | 2.569 |
| sidebar | 16 | go | 10,072 (9,710–10,092) | 1.534 | 2.607 | 4.627 |
| sidebar | 16 | rust | 26,569 (26,383–27,787) | 0.577 | 0.829 | 1.127 |
| search | 16 | go-before | 13,759 (12,765–13,830) | 0.893 | 2.481 | 3.893 |
| search | 16 | go | 16,398 (16,383–17,389) | 0.762 | 2.000 | 3.251 |
| search | 16 | rust | 26,546 (24,570–27,503) | 0.556 | 0.868 | 1.288 |
| avatar | 16 | go-before | 29,846 (28,894–31,295) | 0.425 | 1.029 | 2.083 |
| avatar | 16 | go | 29,035 (28,512–29,068) | 0.438 | 1.113 | 1.956 |
| avatar | 16 | rust | 36,076 (35,705–36,275) | 0.421 | 0.607 | 0.921 |
| static_css | 16 | go-before | 188,614 (172,593–188,828) | 0.064 | 0.145 | 0.360 |
| static_css | 16 | go | 177,664 (176,116–185,410) | 0.065 | 0.161 | 0.397 |
| static_css | 16 | rust | 253,568 (249,394–259,799) | 0.059 | 0.091 | 0.127 |
| post_message | 16 | go-before | 7,408 (7,008–7,479) | 1.919 | 3.781 | 7.199 |
| post_message | 16 | go | 7,192 (6,761–7,545) | 2.005 | 3.513 | 7.415 |
| post_message | 16 | rust | 7,136 (6,234–7,465) | 2.014 | 3.021 | 8.263 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 100 | False | go-before | 2,192.5 (1,999.1–2,218.8) | 219,254 | 13.111 | 19.055 | not measured |
| 100 | False | go | 2,093.6 (2,061.3–2,249.3) | 209,362 | 13.255 | 20.815 | not measured |
| 100 | False | rust | 3,800.9 (3,645.2–3,966.7) | 380,088 | 3.249 | 4.887 | not measured |
| 100 | True | go-before | 1,924.3 (1,833.7–1,940.6) | 192,429 | 10.391 | 14.759 | 509.4 |
| 100 | True | go | 1,923.1 (1,901.5–1,941.8) | 192,307 | 10.111 | 15.423 | 543.2 |
| 100 | True | rust | 2,244.5 (2,230.2–2,294.4) | 224,450 | 5.795 | 8.079 | 523.8 |
| 1,000 | False | go-before | 274.9 (261.8–284.2) | 274,891 | 105.087 | 195.071 | not measured |
| 1,000 | False | go | 277.8 (269.7–278.7) | 277,832 | 161.791 | 254.079 | not measured |
| 1,000 | False | rust | 542.3 (533.9–547.8) | 542,250 | 30.271 | 53.791 | not measured |
| 1,000 | True | go-before | 213.0 (210.9–214.1) | 213,045 | 51.327 | 82.303 | 563.7 |
| 1,000 | True | go | 204.9 (204.0–206.1) | 204,869 | 59.583 | 83.391 | 578.1 |
| 1,000 | True | rust | 239.8 (239.2–240.1) | 239,811 | 44.447 | 69.631 | 559.3 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 33.99 | 84296 | 52.6 | 40.6 | 156.3 | 343.4 | 37.1 |
| go | 32.12 | 84296 | 52.2 | 39.2 | 142.1 | 338.5 | 35.7 |
| rust | 36.42 | 84296 | 36.6 | 42.1 | 125.4 | 252.8 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 439,398 acknowledged HTTP writes verified in both messages and FTS; 27 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
