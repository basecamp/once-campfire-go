# Campfire application benchmark

Release binaries; public HTTP/1.1 listener; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.

3 rotating repetitions; 10.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-3; load generator on CPUs 4-7. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms | CPU µs/request |
|---|---:|---|---:|---:|---:|---:|---:|
| room_show | 16 | ours | 20,626 (19,706–21,784) | 0.534 | 1.354 | 5.191 | 137.2 |
| room_show | 16 | pr-2 | 4,122 (4,058–4,143) | 2.963 | 7.531 | 12.975 | 936.8 |
| room_show | 16 | pr-4 | 4,636 (4,633–4,730) | 2.813 | 7.023 | 11.399 | 851.6 |
| room_show | 16 | pr-5 | 1,005 (952–1,073) | 13.439 | 32.143 | 53.215 | 1240.8 |
| room_show | 16 | pr-6 | 5,622 (5,620–5,685) | 2.143 | 4.915 | 8.099 | 704.1 |
| room_show | 16 | pr-7 | 14,930 (14,735–16,262) | 0.680 | 1.900 | 6.963 | 169.8 |
| room_show | 16 | pr-8 | 4,224 (4,109–4,270) | 3.041 | 7.931 | 12.495 | 943.2 |
| room_show | 16 | rust | 27,491 (26,622–28,503) | 0.494 | 0.932 | 2.013 | 100.9 |
| messages_page | 16 | ours | 20,762 (19,556–21,203) | 0.513 | 1.339 | 5.471 | 137.7 |
| messages_page | 16 | pr-2 | 5,179 (5,129–5,216) | 2.249 | 5.863 | 10.095 | 749.5 |
| messages_page | 16 | pr-4 | 5,585 (5,542–5,638) | 2.415 | 5.791 | 9.279 | 704.4 |
| messages_page | 16 | pr-5 | 5,219 (5,219–5,246) | 2.245 | 5.739 | 10.223 | 748.4 |
| messages_page | 16 | pr-6 | 7,473 (7,470–7,518) | 1.653 | 3.709 | 6.771 | 528.8 |
| messages_page | 16 | pr-7 | 16,104 (15,751–16,241) | 0.641 | 1.775 | 6.727 | 165.4 |
| messages_page | 16 | pr-8 | 5,019 (5,007–5,038) | 2.629 | 6.563 | 10.471 | 791.7 |
| messages_page | 16 | rust | 28,753 (25,372–29,385) | 0.473 | 0.881 | 1.937 | 93.7 |
| search | 16 | ours | 19,094 (18,635–19,554) | 0.634 | 1.472 | 4.559 | 135.6 |
| search | 16 | pr-2 | 6,834 (5,037–6,986) | 1.689 | 4.887 | 9.943 | 514.5 |
| search | 16 | pr-4 | 9,902 (9,649–10,052) | 1.345 | 3.225 | 5.463 | 386.4 |
| search | 16 | pr-5 | 46,396 (45,224–47,270) | 0.234 | 0.582 | 2.441 | 67.1 |
| search | 16 | pr-6 | 12,031 (12,006–12,230) | 0.944 | 2.477 | 4.911 | 321.4 |
| search | 16 | pr-7 | 10,256 (9,641–10,310) | 1.102 | 3.029 | 8.359 | 255.4 |
| search | 16 | pr-8 | 9,086 (9,060–9,640) | 1.460 | 3.521 | 5.775 | 425.1 |
| search | 16 | rust | 23,862 (22,994–24,069) | 0.615 | 0.994 | 1.426 | 118.0 |
| post_message | 16 | ours | 2,414 (2,406–2,564) | 4.931 | 14.191 | 26.815 | 605.8 |
| post_message | 16 | pr-2 | 2,217 (1,797–2,238) | 5.391 | 15.383 | 29.295 | 692.5 |
| post_message | 16 | pr-4 | 3,564 (3,453–3,689) | 4.255 | 6.775 | 10.367 | 562.8 |
| post_message | 16 | pr-5 | 2,440 (2,270–2,452) | 4.859 | 14.119 | 27.023 | 617.1 |
| post_message | 16 | pr-6 | 2,650 (2,637–2,651) | 4.499 | 12.855 | 24.991 | 585.7 |
| post_message | 16 | pr-7 | 2,224 (2,156–2,230) | 5.367 | 15.607 | 29.359 | 707.1 |
| post_message | 16 | pr-8 | 2,792 (2,738–2,854) | 5.563 | 8.223 | 11.783 | 714.0 |
| post_message | 16 | rust | 3,033 (3,021–3,144) | 4.963 | 6.947 | 11.415 | 808.7 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| ours | not validated | — | 55.8 | 41.6 | 141.7 | 141.7 | 36.3 |
| pr-2 | not validated | — | 57.1 | 38.4 | 143.8 | 143.8 | 36.2 |
| pr-4 | not validated | — | 56.0 | 39.5 | 142.3 | 142.3 | 36.9 |
| pr-5 | not validated | — | 58.8 | 38.9 | 140.7 | 140.7 | 36.4 |
| pr-6 | not validated | — | 56.5 | 42.1 | 134.8 | 134.8 | 36.4 |
| pr-7 | not validated | — | 55.9 | 38.6 | 140.9 | 140.9 | 36.3 |
| pr-8 | not validated | — | 57.0 | 36.8 | 175.8 | 175.8 | 36.4 |
| rust | not validated | — | 40.3 | 42.0 | 125.8 | 125.8 | 35.2 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json.gz) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json.gz) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses the public HTTP listener and gzip encoding. It does not measure TLS/ACME or zstd throughput. Active-room CPU includes the concurrent mutation writer.
- Sidebar room-ID checks do not validate application/Turbo-frame layout completeness; verify that scope separately before any cross-port sidebar comparison.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

24 completed application runs; 755,251 acknowledged HTTP posts and 0 active-room posts verified in messages and FTS; 0 validated thumbnail samples. HTTP errors: 0. Cable throughput was not measured.

Initial full response sizes from the first repetition, before workload timing. Message/room ID and byte contracts are recorded in raw.json. Benchmark contracts do not establish browser or strict HTML parity.

| Response | App | Plain bytes | Encoded bytes | Encoding |
|---|---|---:|---:|---|
| room_show | ours | 374,036 | 21289 | gzip |
| messages_page | ours | 342,444 | 12819 | gzip |
| search | ours | 135,497 | 9462 | gzip |
| room_show | pr-2 | 374,036 | 21288 | gzip |
| messages_page | pr-2 | 342,444 | 12819 | gzip |
| search | pr-2 | 135,497 | 9462 | gzip |
| room_show | pr-4 | 374,036 | 21288 | gzip |
| messages_page | pr-4 | 342,444 | 12819 | gzip |
| search | pr-4 | 135,497 | 9462 | gzip |
| room_show | pr-5 | 374,036 | 22414 | gzip |
| messages_page | pr-5 | 342,444 | 12819 | gzip |
| search | pr-5 | 135,497 | 9483 | gzip |
| room_show | pr-6 | 306,756 | 19260 | gzip |
| messages_page | pr-6 | 272,924 | 10772 | gzip |
| search | pr-6 | 112,903 | 8940 | gzip |
| room_show | pr-7 | 374,036 | 22260 | gzip |
| messages_page | pr-7 | 342,444 | 12824 | gzip |
| search | pr-7 | 135,497 | 9895 | gzip |
| room_show | pr-8 | 416,139 | 22084 | gzip |
| messages_page | pr-8 | 383,844 | 13470 | gzip |
| search | pr-8 | 149,625 | 9863 | gzip |
| room_show | rust | 416,139 | 24235 | gzip |
| messages_page | rust | 383,844 | 16158 | gzip |
| search | rust | 149,625 | 9767 | gzip |

Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.
