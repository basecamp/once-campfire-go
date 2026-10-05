# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. 2 application workers on CPUs 0-1; load generator on CPUs 2-3. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 16,707 (16,185–17,142) | 0.764 | 1.872 | 3.531 |
| room_show | 16 | go | 16,583 (16,342–16,599) | 0.780 | 1.904 | 3.591 |
| room_show | 16 | rust | 17,040 (16,498–17,068) | 0.906 | 1.201 | 1.702 |
| messages_page | 16 | go-before | 19,494 (19,229–19,550) | 0.680 | 1.564 | 2.943 |
| messages_page | 16 | go | 19,281 (18,973–19,299) | 0.678 | 1.580 | 3.005 |
| messages_page | 16 | rust | 18,545 (18,420–18,563) | 0.836 | 1.081 | 1.558 |
| sidebar | 16 | go-before | 16,335 (15,824–16,721) | 0.819 | 1.907 | 3.365 |
| sidebar | 16 | go | 16,599 (16,551–16,823) | 0.809 | 1.870 | 3.293 |
| sidebar | 16 | rust | 20,713 (20,391–20,849) | 0.760 | 1.001 | 1.233 |
| search | 16 | go-before | 18,071 (17,540–19,021) | 0.744 | 1.661 | 3.159 |
| search | 16 | go | 17,958 (17,615–18,555) | 0.750 | 1.682 | 3.197 |
| search | 16 | rust | 22,106 (22,031–22,241) | 0.685 | 0.973 | 1.403 |
| post_message | 16 | go-before | 6,740 (6,456–7,022) | 1.966 | 4.831 | 7.959 |
| post_message | 16 | go | 8,301 (8,176–8,418) | 1.570 | 3.789 | 6.791 |
| post_message | 16 | rust | 6,700 (6,481–6,746) | 2.231 | 3.201 | 7.791 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 1,000 | False | go-before | 226.1 (205.1–230.3) | 226,075 | 149.375 | 223.359 | not measured |
| 1,000 | False | go | 389.2 (382.5–393.2) | 389,167 | 40.095 | 56.479 | not measured |
| 1,000 | False | rust | 403.3 (402.7–405.5) | 403,350 | 53.311 | 78.335 | not measured |
| 1,000 | True | go-before | 150.7 (150.5–152.0) | 150,660 | 84.735 | 109.567 | 401.0 |
| 1,000 | True | go | 149.9 (149.9–153.2) | 149,938 | 80.767 | 100.031 | 399.0 |
| 1,000 | True | rust | 167.9 (166.7–168.0) | 167,926 | 91.391 | 132.991 | 391.6 |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 54.24 | 84296 | 51.6 | 40.7 | 137.6 | 265.9 | 36.8 |
| go | 56.04 | 84296 | 51.7 | 41.0 | 135.5 | 269.1 | 36.9 |
| rust | 57.21 | 84296 | 63.8 | 41.0 | 102.6 | 152.9 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples, metadata (source, binary and seed hashes, CPU details, affinity, load averages) and logs were recorded but are not committed.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 443,811 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

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
