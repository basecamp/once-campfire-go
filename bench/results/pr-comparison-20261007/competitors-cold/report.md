# Campfire application benchmark

Release binaries; public HTTP/1.1 listener; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.

3 rotating repetitions; 10.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-3; load generator on CPUs 4-7. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms | CPU µs/request |
|---|---:|---|---:|---:|---:|---:|---:|
| room_show | 16 | ours | 976 (962–981) | 14.975 | 26.591 | 40.319 | 2135.0 |
| room_show | 16 | pr-2 | 277 (274–295) | 51.935 | 89.535 | 134.911 | 9971.3 |
| room_show | 16 | pr-4 | 738 (732–749) | 16.215 | 46.527 | 82.943 | 2707.1 |
| room_show | 16 | pr-5 | 140 (140–150) | 108.799 | 181.119 | 249.983 | 8823.9 |
| room_show | 16 | pr-6 | 478 (478–502) | 29.807 | 50.527 | 73.855 | 5794.5 |
| room_show | 16 | pr-7 | 317 (291–319) | 45.311 | 76.223 | 117.439 | 9270.5 |
| room_show | 16 | pr-8 | 787 (777–795) | 18.303 | 32.607 | 59.231 | 4135.0 |
| room_show | 16 | rust | 1,423 (1,418–1,439) | 10.615 | 17.279 | 24.655 | 2369.6 |
| messages_page | 16 | ours | 1,065 (1,033–1,068) | 14.071 | 23.775 | 34.047 | 1757.7 |
| messages_page | 16 | pr-2 | 300 (293–309) | 48.287 | 82.559 | 121.471 | 9121.2 |
| messages_page | 16 | pr-4 | 719 (706–763) | 16.767 | 47.935 | 83.583 | 2329.2 |
| messages_page | 16 | pr-5 | 192 (186–193) | 77.951 | 142.591 | 208.511 | 7131.0 |
| messages_page | 16 | pr-6 | 542 (526–545) | 26.991 | 44.927 | 65.727 | 4684.2 |
| messages_page | 16 | pr-7 | 318 (296–324) | 44.671 | 79.103 | 116.415 | 8366.6 |
| messages_page | 16 | pr-8 | 880 (864–893) | 16.247 | 30.383 | 49.631 | 3505.2 |
| messages_page | 16 | rust | 1,483 (1,477–1,489) | 10.223 | 16.463 | 23.455 | 2306.2 |
| search | 16 | ours | 2,336 (2,254–2,355) | 5.671 | 12.423 | 21.103 | 798.1 |
| search | 16 | pr-2 | 773 (751–813) | 17.183 | 36.799 | 62.111 | 3363.9 |
| search | 16 | pr-4 | 1,817 (1,728–1,884) | 5.799 | 20.127 | 40.095 | 1073.5 |
| search | 16 | pr-5 | 315 (307–336) | 44.159 | 98.751 | 151.935 | 3624.6 |
| search | 16 | pr-6 | 1,541 (1,335–1,572) | 8.535 | 17.663 | 31.135 | 1795.9 |
| search | 16 | pr-7 | 839 (812–859) | 15.399 | 33.663 | 57.471 | 3088.7 |
| search | 16 | pr-8 | 2,289 (2,273–2,311) | 5.935 | 12.575 | 24.639 | 1351.8 |
| search | 16 | rust | 4,238 (4,230–4,248) | 3.501 | 5.791 | 8.631 | 842.0 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| ours | not validated | — | 63.9 | 40.7 | 61.0 | 61.0 | 36.3 |
| pr-2 | not validated | — | 58.6 | 37.9 | 67.9 | 67.9 | 36.2 |
| pr-4 | not validated | — | 63.5 | 38.9 | 66.7 | 66.7 | 36.9 |
| pr-5 | not validated | — | 64.0 | 38.0 | 70.9 | 70.9 | 36.4 |
| pr-6 | not validated | — | 63.5 | 41.5 | 73.5 | 73.5 | 36.4 |
| pr-7 | not validated | — | 56.4 | 37.8 | 67.2 | 67.2 | 36.3 |
| pr-8 | not validated | — | 55.6 | 36.5 | 70.2 | 70.2 | 36.4 |
| rust | not validated | — | 71.6 | 41.1 | 99.8 | 99.8 | 35.2 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json.gz) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json.gz) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses the public HTTP listener and gzip encoding. It does not measure TLS/ACME or zstd throughput. Active-room CPU includes the concurrent mutation writer.
- Sidebar room-ID checks do not validate application/Turbo-frame layout completeness; verify that scope separately before any cross-port sidebar comparison.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

24 completed application runs; 0 acknowledged HTTP posts and 0 active-room posts verified in messages and FTS; 0 validated thumbnail samples. HTTP errors: 0. Cable throughput was not measured.

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
| search | pr-4 | 135,497 | 9463 | gzip |
| room_show | pr-5 | 374,036 | 22391 | gzip |
| messages_page | pr-5 | 342,444 | 12819 | gzip |
| search | pr-5 | 135,497 | 9462 | gzip |
| room_show | pr-6 | 306,756 | 19260 | gzip |
| messages_page | pr-6 | 272,924 | 10772 | gzip |
| search | pr-6 | 112,903 | 8940 | gzip |
| room_show | pr-7 | 374,036 | 21875 | gzip |
| messages_page | pr-7 | 342,444 | 12819 | gzip |
| search | pr-7 | 135,497 | 9462 | gzip |
| room_show | pr-8 | 416,139 | 22084 | gzip |
| messages_page | pr-8 | 383,844 | 13470 | gzip |
| search | pr-8 | 149,625 | 9861 | gzip |
| room_show | rust | 416,139 | 24235 | gzip |
| messages_page | rust | 383,844 | 16158 | gzip |
| search | rust | 149,625 | 9766 | gzip |

Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.
