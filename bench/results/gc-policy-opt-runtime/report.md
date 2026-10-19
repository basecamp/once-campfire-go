# GC arms: aligned-window per-request accounting

`ce7c42f0d2b2879fb6e6c64a9e473fef30390c8d` (checkout dirty: True), Go 1.27.1, omarchy. Public front listener, gzip, 16 concurrent clients on 12-15, server pinned to 8-11, 2s warmup and 4s timed window per route, 16 repetitions per arm, order rotating cyclically each repetition so every arm occupies every slot. Decoded bodies are equal across every arm and run (normalized for the listener origin and the room page's live cursor). GODEBUG=gctrace=1 in every arm; host load at start: 1.64 2.79 4.40 6/3516 1130114.

Arms: `100` (GOGC=100), `null` (GOGC=100), `200` (GOGC=200), `200+256MiB` (GOGC=200, limit=256MiB). An arm with the same settings as the baseline and a different label is the host noise floor. Binaries, with their hashes, are in `metadata.json`.

Every CPU and GC per-request figure below is an aligned-window measurement: the process CPU delta and the GC-cycle CPU of the timed load-generator window only, divided by that window's requests. GC attribution is **start-attributed and whole-cycle** — the gctrace `@Ts` field is the cycle's start, not its completion, and windows are half-open `[start, end)` — so a cycle beginning in a warmup counts there even if it finishes inside a window, and one beginning in a window counts there in full even if it finishes after. See the correction section at the end.

| Route | Arm | req/s, median [min–max] | CPU µs/req, median [min–max] | GC CPU µs/req, median | GC cycles, median | RSS peak MiB, median [min–max] |
|---|---:|---:|---:|---:|---:|---:|
| room_show | 100 | 135,413 [59,806–136,748] | 27.18 [26.98–33.52] | 2.168 | 1016 | 73.9 [71.6–78.4] |
| room_show | null | 134,046 [124,297–138,004] | 27.32 [26.72–29.64] | 2.170 | 1010 | 74.1 [71.4–77.6] |
| room_show | 200 | 146,346 [68,501–149,846] | 25.48 [24.92–30.36] | 1.095 | 506 | 86.7 [82.6–92.8] |
| room_show | 200+256MiB | 146,091 [129,510–149,690] | 25.54 [25.00–28.88] | 1.096 | 504 | 84.3 [80.3–90.8] |
| messages_page | 100 | 154,110 [68,153–156,560] | 23.92 [23.65–29.05] | 1.656 | 866 | 73.9 [71.6–78.4] |
| messages_page | null | 154,281 [137,741–157,982] | 23.98 [23.43–26.79] | 1.653 | 871 | 74.1 [71.4–77.6] |
| messages_page | 200 | 162,243 [88,000–167,532] | 22.97 [22.26–26.11] | 0.861 | 422 | 86.7 [82.6–92.8] |
| messages_page | 200+256MiB | 162,832 [141,661–166,612] | 22.95 [22.36–26.47] | 0.862 | 421 | 84.3 [80.3–90.8] |
| sidebar | 100 | 155,358 [75,856–157,544] | 22.45 [22.20–27.81] | 1.550 | 808 | 73.9 [71.6–78.4] |
| sidebar | null | 155,017 [138,584–158,344] | 22.48 [22.11–25.51] | 1.549 | 811 | 74.1 [71.4–77.6] |
| sidebar | 200 | 161,942 [81,254–168,647] | 21.78 [20.91–26.06] | 0.814 | 387 | 86.7 [82.6–92.8] |
| sidebar | 200+256MiB | 163,470 [146,432–165,693] | 21.57 [21.31–24.50] | 0.806 | 392 | 84.3 [80.3–90.8] |
| search | 100 | 159,281 [71,115–162,369] | 23.18 [22.79–28.50] | 1.533 | 810 | 73.9 [71.6–78.4] |
| search | null | 160,520 [88,523–163,942] | 23.09 [22.61–27.42] | 1.515 | 812 | 74.1 [71.4–77.6] |
| search | 200 | 166,132 [83,172–170,425] | 22.49 [21.89–26.06] | 0.809 | 392 | 86.7 [82.6–92.8] |
| search | 200+256MiB | 166,970 [155,092–169,122] | 22.33 [22.08–24.14] | 0.798 | 390 | 84.3 [80.3–90.8] |

## Paired ratios against the baseline (same repetition and route)

Ratios to the `100` arm measured in the same repetition and on the same route, and the number of pairs that favour the arm. A throughput ratio above 1 (or a CPU µs/req ratio below 1) with nearly every pair agreeing is a clear sign; mixed signs mean the host noise is larger than the effect. The route pairs of one repetition share a server process and a host window, so they are not independent repetitions: the per-route columns and the pooled row should be read together.

| Route | Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|---:|
| room_show | null | 0.995 [0.928–2.233] | 6/16 | 1.005 [0.812–1.077] | 6/16 | 1.000 | 8/16 |
| room_show | 200 | 1.085 [0.557–2.447] | 14/16 | 0.935 [0.761–1.042] | 14/16 | 0.508 | 15/16 |
| room_show | 200+256MiB | 1.093 [0.947–2.376] | 15/16 | 0.930 [0.775–1.070] | 15/16 | 0.501 | 16/16 |
| messages_page | null | 1.004 [0.883–2.251] | 9/16 | 1.003 [0.827–1.129] | 8/16 | 0.992 | 9/16 |
| messages_page | 200 | 1.061 [0.591–2.379] | 14/16 | 0.954 [0.791–1.050] | 14/16 | 0.508 | 15/16 |
| messages_page | 200+256MiB | 1.060 [0.919–2.416] | 14/16 | 0.956 [0.781–1.108] | 14/16 | 0.512 | 16/16 |
| sidebar | null | 1.003 [0.883–2.002] | 8/16 | 1.001 [0.825–1.141] | 8/16 | 0.989 | 8/16 |
| sidebar | 200 | 1.041 [0.559–2.145] | 15/16 | 0.968 [0.782–1.082] | 15/16 | 0.526 | 15/16 |
| sidebar | 200+256MiB | 1.057 [0.933–2.058] | 15/16 | 0.960 [0.800–1.096] | 15/16 | 0.513 | 16/16 |
| search | null | 1.012 [0.549–2.254] | 11/16 | 0.989 [0.812–1.197] | 9/16 | 0.978 | 9/16 |
| search | 200 | 1.052 [0.516–2.344] | 14/16 | 0.957 [0.786–1.137] | 14/16 | 0.519 | 15/16 |
| search | 200+256MiB | 1.050 [0.976–2.348] | 15/16 | 0.963 [0.784–1.037] | 15/16 | 0.518 | 16/16 |

Pooled over all routes and repetitions (all pairs, not independent):

| Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median [min–max] | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|
| null | 1.001 [0.549–2.254] | 34/64 | 1.001 [0.812–1.197] | 31/64 | 0.996 [0.317–2.563] | 34/64 |
| 200 | 1.056 [0.516–2.447] | 57/64 | 0.957 [0.761–1.137] | 57/64 | 0.516 [0.166–1.474] | 60/64 |
| 200+256MiB | 1.061 [0.919–2.416] | 59/64 | 0.955 [0.775–1.108] | 59/64 | 0.512 [0.160–0.617] | 64/64 |

## Correction: aligned window vs whole-process CPU / timed requests

The first version of this script divided the whole process's CPU time (and the whole log's GC CPU) by the timed requests only, so the warmup windows and the setup requests were in the numerator and not in the denominator. The aligned figure is the one to quote; the whole-run figure is shown to make the size of the correction visible. RSS and the GC cycle counts are unaffected.

| Arm | CPU µs/req, aligned | CPU µs/req, whole-run (biased) | bias | GC µs/req, aligned | GC µs/req, whole-run (biased) | bias |
|---|---:|---:|---:|---:|---:|---:|
| 100 | 23.84 | 33.92 | 1.42× | 1.645 | 2.433 | 1.48× |
| null | 23.82 | 34.08 | 1.43× | 1.632 | 2.439 | 1.49× |
| 200 | 22.66 | 32.25 | 1.42× | 0.842 | 1.252 | 1.49× |
| 200+256MiB | 22.68 | 32.29 | 1.42× | 0.847 | 1.240 | 1.46× |

Per-window samples (route, requests, aligned CPU and GC deltas, window bounds), per-run RSS, CPU, GC totals, decoded body hashes and log volumes are in `raw.json`; binary hashes, seed hash and host identity in `metadata.json`; the GC and startup lines of each server run in `*.log`. The per-request access log is left on (harness fidelity) and only its line and byte counts are kept. This is a workstation measurement on a shared host; medians, spread and paired signs are the evidence, not single samples.

