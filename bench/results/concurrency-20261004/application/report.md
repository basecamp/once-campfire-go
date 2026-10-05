# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. 2 application workers on CPUs 0-1; load generator on CPUs 2-3. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 9,456 (9,371–9,521) | 1.464 | 3.147 | 5.287 |
| room_show | 16 | go | 16,924 (16,644–17,148) | 0.761 | 1.861 | 3.409 |
| room_show | 16 | rust | 17,324 (17,190–17,379) | 0.896 | 1.173 | 1.635 |
| messages_page | 16 | go-before | 13,430 (13,218–13,589) | 1.003 | 2.321 | 4.175 |
| messages_page | 16 | go | 19,335 (18,982–19,855) | 0.673 | 1.561 | 3.025 |
| messages_page | 16 | rust | 18,940 (18,498–18,942) | 0.823 | 1.053 | 1.398 |
| sidebar | 16 | go-before | 11,934 (11,729–12,538) | 1.159 | 2.443 | 4.279 |
| sidebar | 16 | go | 16,446 (16,052–16,475) | 0.825 | 1.896 | 3.367 |
| sidebar | 16 | rust | 21,002 (20,420–21,104) | 0.750 | 0.985 | 1.209 |
| search | 16 | go-before | 9,073 (9,002–9,361) | 1.505 | 3.377 | 5.687 |
| search | 16 | go | 18,800 (18,764–18,847) | 0.721 | 1.591 | 2.993 |
| search | 16 | rust | 22,942 (22,854–23,154) | 0.669 | 0.918 | 1.275 |
| post_message | 16 | go-before | 4,540 (4,531–4,583) | 2.549 | 7.663 | 12.967 |
| post_message | 16 | go | 7,086 (6,890–7,100) | 1.921 | 4.471 | 7.427 |
| post_message | 16 | rust | 6,739 (6,739–6,816) | 2.207 | 3.173 | 7.739 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|
| 1,000 | False | go-before | 228.3 (222.4–230.3) | 228,262 | 228.991 | 355.327 | not measured |
| 1,000 | False | go | 232.6 (229.0–233.8) | 232,606 | 143.359 | 235.775 | not measured |
| 1,000 | False | rust | 409.3 (407.3–415.4) | 409,349 | 50.847 | 71.231 | not measured |

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 55.33 | 84296 | 51.6 | 40.3 | 133.2 | 265.9 | 36.2 |
| go | 54.68 | 84296 | 52.0 | 40.9 | 136.8 | 266.7 | 36.8 |
| rust | 56.54 | 84296 | 63.8 | 40.9 | 101.9 | 152.3 | 35.1 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- Raw samples, metadata (source, binary and seed hashes, CPU details, affinity, load averages) and logs were recorded but are not committed.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 377,158 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Incomplete throughput deliveries: 0.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 9,462 |
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
