# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 alternating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go | 9,570 (9,154–9,716) | 1.161 | 3.455 | 7.627 |
| room_show | 16 | rust | 8,533 (8,113–8,569) | 1.801 | 2.627 | 3.625 |
| messages_page | 16 | go | 12,028 (11,327–12,144) | 0.910 | 2.639 | 6.543 |
| messages_page | 16 | rust | 9,214 (9,187–9,720) | 1.648 | 2.383 | 3.541 |
| sidebar | 16 | go | 16,187 (15,460–16,271) | 0.666 | 1.888 | 5.563 |
| sidebar | 16 | rust | 12,569 (10,505–12,766) | 1.223 | 1.780 | 2.421 |
| search | 16 | go | 12,261 (11,405–12,736) | 0.994 | 2.421 | 5.507 |
| search | 16 | rust | 9,258 (8,538–10,712) | 1.568 | 2.523 | 4.025 |
| post_message | 16 | go | 3,483 (3,344–3,504) | 3.359 | 9.583 | 18.927 |
| post_message | 16 | rust | 3,438 (2,904–3,581) | 4.367 | 5.751 | 13.119 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go | 157.41 | 84296 | 104.1 | 37.6 | 123.9 | 169.6 | 37.8 |
| rust | 150.83 | 84296 | 87.6 | 39.3 | 101.1 | 148.9 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

6 completed application runs; 136,104 acknowledged HTTP writes verified in both messages and FTS; 6 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go | 306,756 |
| messages_page | go | 272,924 |
| sidebar | go | 9,462 |
| search | go | 112,903 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.

[Harness hashes and build commands](harness.json). Native binary sizes reflect these build flags; the production Go image strips symbols. Server/load-generator logs are retained alongside the raw JSON (gzip-compressed after measurement when archived).

[Interrupted repetitions and resumption](CONTENTION.md): incomplete contention-affected attempts were excluded and retried with unchanged binaries and settings. Completed samples were retained. See that record for host-noise limits.
