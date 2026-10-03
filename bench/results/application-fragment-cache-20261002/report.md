# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions, 2-second samples after 2-second warmups; server CPUs 8-11, load-generator CPUs 12-15. Zero HTTP transport/status errors required; Cable requires every posted message to reach every client. Raw samples and source/binary hashes are included.

| Workload | Clients | Rust req/s | Go req/s | Go / Rust | Rust p99 ms | Go p99 ms |
|---|---:|---:|---:|---:|---:|---:|

Cable, uploads, startup, memory, sample spread and load averages are recorded in the raw JSON. These measurements cover the listed workloads, not all possible deployment behavior.
