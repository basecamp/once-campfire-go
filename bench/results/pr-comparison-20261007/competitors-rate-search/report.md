# Campfire application benchmark

Release binaries; public HTTP/1.1 listener; gzip encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.

3 rotating repetitions; 10.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 0-3; load generator on CPUs 4-7. HTTP uses gzip encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Fixed-rate arrivals are independent of completions. Latency starts at the scheduled arrival, including generator lateness and client queueing; service latency alone would omit these delays. Quantiles are upper bucket bounds (1 µs resolution below 1 ms, <0.2% above). Successful-response percentiles must be read with error/drop/expiry counters in raw samples. Samples with any such failures are rejected and retained separately. Throughput includes drain time when completion exceeds the arrival window. Generator lateness and queue delay disclose client-side limitations; these are not server-only latency measurements.

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Max clients | Offered req/s | App | Successful req/s (range) | Scheduled p50 ms | p95 ms | p99 ms | Generator p99 ms | Queue p99 ms | Service p99 ms | CPU µs/request |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| search | 16 | 5000 | ours | 4,999 (4,998–5,000) | 0.914 | 4.328 | 5.688 | 4.020 | 0.704 | 3.536 | 174.6 |
| search | 16 | 5000 | pr-2 | 4,862 (4,431–4,998) | 299.520 | 662.528 | 678.912 | 2.928 | 674.816 | 11.824 | 657.2 |
| search | 16 | 5000 | pr-4 | 4,999 (4,999–5,000) | 0.851 | 5.072 | 9.312 | 3.404 | 3.252 | 6.000 | 388.0 |
| search | 16 | 5000 | pr-5 | 4,999 (4,997–5,000) | 0.970 | 4.440 | 6.648 | 4.168 | 1.896 | 4.068 | 147.6 |
| search | 16 | 5000 | pr-6 | 4,999 (4,993–4,999) | 0.707 | 4.592 | 7.120 | 3.368 | 1.702 | 5.040 | 322.8 |
| search | 16 | 5000 | pr-7 | 4,999 (4,992–5,000) | 1.892 | 7.504 | 13.008 | 3.440 | 7.616 | 8.928 | 302.2 |
| search | 16 | 5000 | pr-8 | 4,999 (4,999–5,000) | 0.920 | 5.056 | 7.552 | 3.392 | 1.556 | 5.728 | 408.8 |
| search | 16 | 5000 | rust | 5,000 (4,998–5,000) | 0.780 | 3.956 | 5.016 | 4.104 | 0.800 | 1.266 | 146.0 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| ours | not validated | — | 64.1 | 41.1 | 51.5 | 51.5 | 36.3 |
| pr-2 | not validated | — | 58.5 | 38.2 | 61.8 | 61.8 | 36.2 |
| pr-4 | not validated | — | 56.8 | 38.7 | 65.3 | 65.3 | 36.9 |
| pr-5 | not validated | — | 61.7 | 38.2 | 53.9 | 53.9 | 36.4 |
| pr-6 | not validated | — | 80.5 | 41.8 | 66.0 | 66.0 | 36.4 |
| pr-7 | not validated | — | 56.3 | 38.0 | 58.7 | 58.7 | 36.3 |
| pr-8 | not validated | — | 55.7 | 36.0 | 72.3 | 72.3 | 36.4 |
| rust | not validated | — | 39.2 | 41.3 | 50.4 | 50.4 | 35.2 |

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
| search | ours | 135,497 | 9462 | gzip |
| search | pr-2 | 135,497 | 9462 | gzip |
| search | pr-4 | 135,497 | 9462 | gzip |
| search | pr-5 | 135,497 | 9495 | gzip |
| search | pr-6 | 112,903 | 8940 | gzip |
| search | pr-7 | 135,497 | 9895 | gzip |
| search | pr-8 | 149,625 | 9862 | gzip |
| search | rust | 149,625 | 9766 | gzip |

Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.
