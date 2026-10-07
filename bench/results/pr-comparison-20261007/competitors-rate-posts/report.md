# Campfire application benchmark

Release binaries; public HTTP/1.1 listener; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.

3 rotating repetitions; 10.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-3; load generator on CPUs 4-7. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Fixed-rate arrivals are independent of completions. Latency starts at the scheduled arrival, including generator lateness and client queueing; service latency alone would omit these delays. Quantiles are upper bucket bounds (1 µs resolution below 1 ms, <0.2% above). Successful-response percentiles must be read with error/drop/expiry counters in raw samples. Samples with any such failures are rejected and retained separately. Throughput includes drain time when completion exceeds the arrival window. Generator lateness and queue delay disclose client-side limitations; these are not server-only latency measurements.

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Max clients | Offered req/s | App | Successful req/s (range) | Scheduled p50 ms | p95 ms | p99 ms | Generator p99 ms | Queue p99 ms | Service p99 ms | CPU µs/request |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| post_message | 16 | 1000 | ours | 1,000 (1,000–1,000) | 1.916 | 5.952 | 9.088 | 4.352 | 0.075 | 7.440 | 824.0 |
| post_message | 16 | 1000 | pr-2 | 1,000 (1,000–1,000) | 1.940 | 5.808 | 9.760 | 4.304 | 0.072 | 7.536 | 818.0 |
| post_message | 16 | 1000 | pr-4 | 1,000 (1,000–1,000) | 1.938 | 5.472 | 7.336 | 4.060 | 0.083 | 5.200 | 868.0 |
| post_message | 16 | 1000 | pr-5 | 1,000 (1,000–1,000) | 1.812 | 5.712 | 8.512 | 4.060 | 0.067 | 7.224 | 839.0 |
| post_message | 16 | 1000 | pr-6 | 1,000 (1,000–1,000) | 1.834 | 5.456 | 7.512 | 4.216 | 0.072 | 5.776 | 834.0 |
| post_message | 16 | 1000 | pr-7 | 1,000 (1,000–1,000) | 1.928 | 5.656 | 9.104 | 4.120 | 0.072 | 7.176 | 831.0 |
| post_message | 16 | 1000 | pr-8 | 1,000 (1,000–1,000) | 1.948 | 5.440 | 7.168 | 4.336 | 0.070 | 5.008 | 908.0 |
| post_message | 16 | 1000 | rust | 1,000 (1,000–1,000) | 1.944 | 5.440 | 7.768 | 4.336 | 0.079 | 5.040 | 993.0 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| ours | not validated | — | 58.0 | 41.0 | 132.5 | 132.5 | 36.3 |
| pr-2 | not validated | — | 60.6 | 38.2 | 134.0 | 134.0 | 36.2 |
| pr-4 | not validated | — | 59.6 | 38.9 | 133.4 | 133.4 | 36.9 |
| pr-5 | not validated | — | 58.2 | 37.9 | 135.5 | 135.5 | 36.4 |
| pr-6 | not validated | — | 58.0 | 41.8 | 130.3 | 130.3 | 36.4 |
| pr-7 | not validated | — | 58.9 | 37.7 | 134.0 | 134.0 | 36.3 |
| pr-8 | not validated | — | 29.5 | 36.7 | 163.4 | 163.4 | 36.4 |
| rust | not validated | — | 67.9 | 41.2 | 112.0 | 112.0 | 35.2 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json.gz) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json.gz) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses the public HTTP listener and gzip encoding. It does not measure TLS/ACME or zstd throughput. Active-room CPU includes the concurrent mutation writer.
- Sidebar room-ID checks do not validate application/Turbo-frame layout completeness; verify that scope separately before any cross-port sidebar comparison.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

24 completed application runs; 365,030 acknowledged HTTP posts and 0 active-room posts verified in messages and FTS; 0 validated thumbnail samples. HTTP errors: 0. Cable throughput was not measured.

Initial full response sizes from the first repetition, before workload timing. Message/room ID and byte contracts are recorded in raw.json. Benchmark contracts do not establish browser or strict HTML parity.

| Response | App | Plain bytes | Encoded bytes | Encoding |
|---|---|---:|---:|---|

Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.
