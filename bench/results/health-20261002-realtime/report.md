# Go vs Rust: health endpoint only

**This is not an application performance comparison.** The Go port is incomplete. Room pages, messages, search, media and Cable have not passed parity; no speed claim about those workloads follows from this report.

Rust `64f86353021145b63849fb1cd93adeb08f3b8dbb`, Go 1.27.1. Native release binaries on cph-b395, four pinned hardware threads (8-11); load generator on 12-15. Each repetition uses a fresh SQLite backup of the same seed. Apps run sequentially in alternating order. Two-second warmup, 5-second samples, 3 repetitions. HTTP/1.1 keep-alive, identity encoding, direct application listeners. Both bodies are byte-identical (73 bytes), with status 200 and zero transport errors in every sample.

| Clients | Rust req/s, median [min–max] | Go req/s, median [min–max] | Go / Rust | Rust p99 ms, median | Go p99 ms, median |
|---:|---:|---:|---:|---:|---:|
| 1 | 57,405 [57,396–57,992] | 68,598 [68,528–69,692] | 1.19× | 0.024 | 0.022 |
| 16 | 343,168 [340,788–343,802] | 428,723 [417,306–429,092] | 1.25× | 0.085 | 0.138 |
| 64 | 358,576 [339,843–359,030] | 453,756 [451,096–454,421] | 1.27× | 0.362 | 0.654 |

Raw samples are in `raw.json`; CPU topology, binary hashes/sizes, toolchains, seed hash, load average and settings are in `metadata.json`. Startup and Pss are recorded for diagnostics only: the apps currently initialize different feature sets, so those values are not comparable as full application footprints. Rust also starts its unused front listener. The load generator validates status and average body length throughout, and the harness checks exact body bytes before every sample group. This is a workstation measurement; background load is not controlled.
