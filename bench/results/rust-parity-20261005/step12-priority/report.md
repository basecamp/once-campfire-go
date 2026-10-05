# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

3 rotating repetitions; 5.0-second HTTP samples after 2-second warmups. Four application workers on CPUs 8-11; load generator on CPUs 12-15. HTTP uses identity encoding. Every run starts with a separate copy of the same seed.

HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.

## HTTP

Rates show median (minimum–maximum); latency is the median of each run’s percentile.

| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms |
|---|---:|---|---:|---:|---:|---:|
| room_show | 16 | go-before | 4,173 (4,105–4,867) | 2.891 | 7.479 | 14.047 |
| room_show | 16 | go | 7,408 (6,531–7,611) | 1.536 | 4.499 | 8.855 |
| room_show | 16 | rust | 7,246 (7,004–7,286) | 2.101 | 3.141 | 4.811 |
| messages_page | 16 | go-before | 5,923 (4,926–5,988) | 2.119 | 5.163 | 10.399 |
| messages_page | 16 | go | 8,827 (7,887–9,039) | 1.295 | 3.695 | 7.535 |
| messages_page | 16 | rust | 8,240 (7,932–9,071) | 1.838 | 2.697 | 3.859 |
| sidebar | 16 | go-before | 5,272 (4,919–6,244) | 2.659 | 5.071 | 9.599 |
| sidebar | 16 | go | 10,128 (10,112–12,406) | 0.941 | 3.541 | 8.003 |
| sidebar | 16 | rust | 11,914 (11,846–12,982) | 1.283 | 1.884 | 2.593 |
| search | 16 | go-before | 4,873 (4,303–5,080) | 2.719 | 6.007 | 11.295 |
| search | 16 | go | 7,566 (7,229–8,464) | 1.551 | 4.271 | 8.695 |
| search | 16 | rust | 10,752 (9,853–11,737) | 1.385 | 2.119 | 3.283 |
| post_message | 16 | go-before | 1,959 (1,817–1,961) | 5.331 | 18.207 | 31.375 |
| post_message | 16 | go | 1,930 (753–2,189) | 4.451 | 12.103 | 30.639 |
| post_message | 16 | rust | 2,840 (2,539–3,139) | 4.723 | 7.667 | 15.191 |

## Action Cable

Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.

| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |
|---:|---|---|---:|---:|---:|---:|---:|

## Media and resources

| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| go-before | 129.37 | 84296 | 68.4 | 35.5 | 128.3 | 173.0 | 37.7 |
| go | 150.00 | 84296 | 78.9 | 38.8 | 127.0 | 172.1 | 37.8 |
| rust | 153.75 | 84296 | 80.8 | 40.8 | 100.4 | 149.0 | 35.6 |

Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.

## Reproduction and limits

- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.
- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.
- This comparison uses direct application HTTP listeners. TLS, ACME, gzip/zstd and the public response cache have separate functional tests; these numbers do not measure their throughput.
- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.

## Validation totals

9 completed application runs; 130,655 acknowledged HTTP writes verified in both messages and FTS; 9 real thumbnail samples with identical hashes. HTTP errors: 0. Cable throughput was not measured.

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

[Harness hashes and build commands](harness.json). Native binary sizes reflect these build flags; the production Go image strips symbols. Server/load-generator logs are retained alongside the raw JSON (gzip-compressed after measurement when archived).

[Interrupted repetitions and resumption](CONTENTION.md): incomplete contention-affected attempts were excluded and retried with unchanged binaries and settings. Completed samples were retained. See that record for host-noise limits.
