# Campfire application benchmark

Release binaries; public HTTP/1.1 listener; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.

3 rotating repetitions; 10.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-3; load generator on CPUs 4-7. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Fixed-rate arrivals are independent of completions. Latency starts at the scheduled arrival, including generator lateness and client queueing; service latency alone would omit these delays. Quantiles are upper bucket bounds (1 µs resolution below 1 ms, <0.2% above). Successful-response percentiles must be read with error/drop/expiry counters in raw samples. Samples with any such failures are rejected and retained separately. Throughput includes drain time when completion exceeds the arrival window. Generator lateness and queue delay disclose client-side limitations; these are not server-only latency measurements.

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Max clients | Offered req/s | App | Successful req/s (range) | Scheduled p50 ms | p95 ms | p99 ms | Generator p99 ms | Queue p99 ms | Service p99 ms | CPU µs/request |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| room_show | 16 | 500 | ours | 500 (500–500) | 2.124 | 5.984 | 10.576 | 5.216 | 0.352 | 7.064 | 766.0 |
| room_show | 16 | 500 | pr-2 | 500 (500–500) | 2.548 | 6.008 | 8.608 | 4.036 | 0.048 | 6.944 | 1610.0 |
| room_show | 16 | 500 | pr-4 | 500 (500–500) | 2.468 | 5.984 | 8.448 | 4.296 | 0.118 | 5.760 | 1574.0 |
| room_show | 16 | 500 | pr-5 | 500 (500–500) | 4.608 | 12.736 | 20.032 | 4.048 | 0.191 | 18.464 | 1648.0 |
| room_show | 16 | 500 | pr-6 | 500 (500–500) | 2.588 | 6.264 | 9.328 | 4.080 | 0.136 | 6.552 | 1478.0 |
| room_show | 16 | 500 | pr-7 | 500 (500–500) | 2.022 | 5.432 | 8.720 | 4.424 | 0.339 | 5.256 | 808.0 |
| room_show | 16 | 500 | pr-8 | 500 (500–500) | 2.424 | 5.968 | 7.312 | 4.320 | 0.059 | 5.200 | 1634.0 |
| room_show | 16 | 500 | rust | 500 (500–500) | 2.204 | 5.496 | 7.504 | 4.424 | 0.341 | 3.996 | 750.0 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| ours | not validated | — | 57.5 | 40.6 | 48.6 | 48.6 | 36.3 |
| pr-2 | not validated | — | 61.8 | 38.1 | 58.5 | 58.5 | 36.2 |
| pr-4 | not validated | — | 57.1 | 38.6 | 56.6 | 56.6 | 36.9 |
| pr-5 | not validated | — | 57.9 | 37.7 | 54.8 | 54.8 | 36.4 |
| pr-6 | not validated | — | 57.4 | 41.9 | 63.2 | 63.2 | 36.4 |
| pr-7 | not validated | — | 58.2 | 38.3 | 49.4 | 49.4 | 36.3 |
| pr-8 | not validated | — | 58.0 | 36.1 | 73.0 | 73.0 | 36.4 |
| rust | not validated | — | 72.1 | 41.2 | 44.9 | 44.9 | 35.2 |

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
| room_show | pr-2 | 374,036 | 21288 | gzip |
| room_show | pr-4 | 374,036 | 21288 | gzip |
| room_show | pr-5 | 374,036 | 22409 | gzip |
| room_show | pr-6 | 306,756 | 19260 | gzip |
| room_show | pr-7 | 374,036 | 22260 | gzip |
| room_show | pr-8 | 416,139 | 22085 | gzip |
| room_show | rust | 416,139 | 24234 | gzip |

Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.
