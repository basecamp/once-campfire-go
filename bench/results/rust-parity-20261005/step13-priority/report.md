# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 3,896 (3,206–4,107) | 3.319 | 7.691 | 15.039 |
| room_show | 16 | go | 5,694 (5,298–7,838) | 2.067 | 5.591 | 11.455 |
| room_show | 16 | rust | 7,770 (7,530–8,295) | 1.958 | 2.925 | 4.119 |
| messages_page | 16 | go-before | 4,162 (3,991–4,763) | 2.971 | 7.507 | 13.983 |
| messages_page | 16 | go | 8,308 (6,777–8,896) | 1.371 | 3.875 | 8.319 |
| messages_page | 16 | rust | 8,371 (7,632–8,478) | 1.828 | 2.665 | 3.925 |
| sidebar | 16 | go-before | 5,753 (3,757–6,506) | 2.447 | 4.431 | 8.067 |
| sidebar | 16 | go | 13,939 (8,792–14,631) | 0.776 | 2.267 | 6.155 |
| sidebar | 16 | rust | 11,183 (10,338–11,354) | 1.362 | 2.001 | 2.825 |
| search | 16 | go-before | 4,360 (3,705–5,063) | 3.067 | 6.671 | 12.159 |
| search | 16 | go | 10,022 (7,692–10,302) | 1.179 | 3.107 | 6.719 |
| search | 16 | rust | 8,418 (7,629–10,274) | 1.711 | 2.813 | 4.975 |
| post_message | 16 | go-before | 1,139 (597–1,982) | 7.579 | 28.959 | 55.263 |
| post_message | 16 | go | 2,878 (1,463–3,052) | 4.017 | 11.295 | 22.399 |
| post_message | 16 | rust | 1,836 (1,456–3,168) | 5.399 | 9.015 | 31.599 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 210.58 | 84296 | 151.7 | 36.6 | 125.6 | 171.2 | 37.7 |
| go | 157.07 | 84296 | 133.0 | 38.5 | 131.5 | 178.5 | 37.8 |
| rust | 211.82 | 84296 | 118.9 | 40.6 | 98.8 | 146.0 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 116,591 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

Generated HTML has different whitespace, attributes and serialization. Representative body sizes from the first repetition are shown below; comparison contracts use matching message/room IDs and the separately verified browser workflows.

| Response | App | Bytes |
|---|---|---:|
| room_show | go-before | 374,036 |
| messages_page | go-before | 342,444 |
| sidebar | go-before | 9,462 |
| search | go-before | 135,497 |
| room_show | go | 374,036 |
| messages_page | go | 342,444 |
| sidebar | go | 9,462 |
| search | go | 135,497 |
| room_show | rust | 416,139 |
| messages_page | rust | 383,844 |
| sidebar | rust | 30,763 |
| search | rust | 149,625 |

Default application logging is retained: Rust logs completed jobs; Go logs startup and job failures. Native media libraries are identical between the two applications in this run. Container byte goldens use the separately pinned toolchain.
