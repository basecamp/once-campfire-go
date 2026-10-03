# Go vs Rust: health endpoint only

**This is not an application performance comparison.** The Go port is incomplete. Room pages, messages, search, media and Cable have not passed parity; no speed claim about those workloads follows from this report.

Rust `64f86353021145b63849fb1cd93adeb08f3b8dbb`, Go 1.27.1. Native release binaries on cph-b395, four pinned hardware threads (8-11); load generator on 12-15. Each repetition uses a fresh SQLite backup of the same seed. Apps run sequentially in alternating order. Two-second warmup, 5-second samples, 3 repetitions. HTTP/1.1 keep-alive, identity encoding, direct application listeners. Both bodies are byte-identical (73 bytes), with status 200 and zero transport errors in every sample.

| Clients | Rust req/s, median [min–max] | Go req/s, median [min–max] | Go / Rust | Rust p99 ms, median | Go p99 ms, median |
|---:|---:|---:|---:|---:|---:|
| 1 | 53,448 [52,211–56,902] | 38,434 [35,954–61,393] | 0.72× | 0.028 | 0.056 |
| 16 | 314,045 [313,210–328,337] | 298,111 [155,199–415,264] | 0.95× | 0.114 | 0.219 |
| 64 | 339,268 [238,055–347,353] | 363,893 [254,056–420,728] | 1.07× | 0.408 | 0.767 |

Raw samples are in `raw.json`; CPU topology, binary hashes/sizes, toolchains, seed hash, load average and settings are in `metadata.json`. Startup and Pss are recorded for diagnostics only: the apps currently initialize different feature sets, so those values are not comparable as full application footprints. Rust also starts its unused front listener. The load generator validates status and average body length throughout, and the harness checks exact body bytes before every sample group. This is a workstation measurement; background load is not controlled.
