# Go and Rust application benchmark

Native release binaries; direct HTTP/1.1 application listeners; identity encoding; identical seed and CPU affinity. Media use installed native libraries. Workstation background load recorded.

1 alternating repetitions, 2-second samples after 2-second warmups; server CPUs 8-11, load-generator CPUs 12-15. Zero HTTP transport/status errors required; Cable requires every posted message to reach every client. Raw samples and source/binary hashes are included.

| Workload | Clients | Rust req/s | Go req/s | Go / Rust | Rust p99 ms | Go p99 ms |
|---|---:|---:|---:|---:|---:|---:|
| room_show | 1 | 5,753 | 57 | 0.01× | 0.288 | 18.783 |
| room_show | 16 | 27,705 | 214 | 0.01× | 1.054 | 191.743 |
| messages_page | 1 | 6,984 | 62 | 0.01× | 0.252 | 17.455 |
| messages_page | 16 | 31,729 | 230 | 0.01× | 0.884 | 161.663 |
| sidebar | 1 | 9,025 | 535 | 0.06× | 0.208 | 2.315 |
| sidebar | 16 | 38,611 | 2,091 | 0.05× | 0.755 | 22.511 |
| search | 1 | 9,477 | 178 | 0.02× | 0.165 | 6.411 |
| search | 16 | 30,550 | 643 | 0.02× | 1.162 | 62.559 |
| avatar | 1 | 14,811 | 2,524 | 0.17× | 0.096 | 0.567 |
| avatar | 16 | 56,854 | 10,278 | 0.18× | 0.580 | 5.419 |
| static_css | 1 | 74,965 | 62,352 | 0.83× | 0.017 | 0.031 |
| static_css | 16 | 502,586 | 306,411 | 0.61× | 0.052 | 0.208 |
| post_message | 1 | 3,701 | 1,007 | 0.27× | 1.887 | 4.359 |
| post_message | 16 | 8,081 | 2,686 | 0.33× | 5.603 | 21.167 |

Cable, uploads, startup, memory, sample spread and load averages are recorded in the raw JSON. These measurements cover the listed workloads, not all possible deployment behavior.
