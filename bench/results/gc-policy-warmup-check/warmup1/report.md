# GC arms: aligned-window per-request accounting

`ce7c42f0d2b2879fb6e6c64a9e473fef30390c8d` (checkout dirty: True), Go 1.27.1, omarchy. Public front listener, gzip, 16 concurrent clients on 12-15, server pinned to 8-11, 1s warmup and 3s timed window per route, 5 repetitions per arm, order rotating cyclically each repetition so every arm occupies every slot. Decoded bodies are equal across every arm and run (normalized for the listener origin and the room page's live cursor). GODEBUG=gctrace=1 in every arm; host load at start: 2.69 4.12 5.42 6/3768 1200800.

Arms: `100` (GOGC=100), `200` (GOGC=200). An arm with the same settings as the baseline and a different label is the host noise floor. Binaries, with their hashes, are in `metadata.json`.

Every CPU and GC per-request figure below is an aligned-window measurement: the process CPU delta and the GC-cycle CPU of the timed load-generator window only, divided by that window's requests. GC attribution is **start-attributed and whole-cycle** — the gctrace `@Ts` field is the cycle's start, not its completion, and windows are half-open `[start, end)` — so a cycle beginning in a warmup counts there even if it finishes inside a window, and one beginning in a window counts there in full even if it finishes after. See the correction section at the end.

| Route | Arm | req/s, median [min–max] | CPU µs/req, median [min–max] | GC CPU µs/req, median | GC cycles, median | RSS peak MiB, median [min–max] |
|---|---:|---:|---:|---:|---:|---:|
| room_show | 100 | 131,463 [128,398–133,774] | 27.56 [27.43–28.61] | 2.304 | 827 | 73.3 [70.9–75.5] |
| room_show | 200 | 144,338 [141,776–147,955] | 25.75 [25.23–26.23] | 1.169 | 417 | 84.4 [82.7–88.5] |
| messages_page | 100 | 148,730 [140,902–152,396] | 24.61 [24.23–26.02] | 1.825 | 723 | 73.3 [70.9–75.5] |
| messages_page | 200 | 159,267 [157,705–161,235] | 23.42 [23.07–23.63] | 0.928 | 358 | 84.4 [82.7–88.5] |
| sidebar | 100 | 155,091 [151,270–156,220] | 22.63 [22.30–23.16] | 1.498 | 617 | 73.3 [70.9–75.5] |
| sidebar | 200 | 163,908 [161,389–166,101] | 21.61 [21.21–21.81] | 0.771 | 299 | 84.4 [82.7–88.5] |
| search | 100 | 155,461 [154,578–158,384] | 23.63 [23.27–23.87] | 1.663 | 688 | 73.3 [70.9–75.5] |
| search | 200 | 164,004 [158,513–166,177] | 22.70 [22.48–23.40] | 0.882 | 328 | 84.4 [82.7–88.5] |

## Paired ratios against the baseline (same repetition and route)

Ratios to the `100` arm measured in the same repetition and on the same route, and the number of pairs that favour the arm. A throughput ratio above 1 (or a CPU µs/req ratio below 1) with nearly every pair agreeing is a clear sign; mixed signs mean the host noise is larger than the effect. The route pairs of one repetition share a server process and a host window, so they are not independent repetitions: the per-route columns and the pooled row should be read together.

| Route | Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|---:|
| room_show | 200 | 1.098 [1.086–1.121] | 5/5 | 0.932 [0.906–0.935] | 5/5 | 0.501 | 5/5 |
| messages_page | 200 | 1.077 [1.058–1.130] | 5/5 | 0.943 [0.900–0.958] | 5/5 | 0.508 | 5/5 |
| sidebar | 200 | 1.061 [1.034–1.097] | 5/5 | 0.949 [0.919–0.978] | 5/5 | 0.512 | 5/5 |
| search | 200 | 1.042 [1.020–1.055] | 5/5 | 0.969 [0.961–0.989] | 5/5 | 0.534 | 5/5 |

Pooled over all routes and repetitions (all pairs, not independent):

| Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median [min–max] | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|
| 200 | 1.062 [1.020–1.130] | 20/20 | 0.948 [0.900–0.989] | 20/20 | 0.511 [0.477–0.550] | 20/20 |

## Correction: aligned window vs whole-process CPU / timed requests

The first version of this script divided the whole process's CPU time (and the whole log's GC CPU) by the timed requests only, so the warmup windows and the setup requests were in the numerator and not in the denominator. The aligned figure is the one to quote; the whole-run figure is shown to make the size of the correction visible. RSS and the GC cycle counts are unaffected.

| Arm | CPU µs/req, aligned | CPU µs/req, whole-run (biased) | bias | GC µs/req, aligned | GC µs/req, whole-run (biased) | bias |
|---|---:|---:|---:|---:|---:|---:|
| 100 | 24.05 | 31.29 | 1.30× | 1.735 | 2.340 | 1.35× |
| 200 | 23.16 | 29.57 | 1.28× | 0.921 | 1.185 | 1.29× |

Per-window samples (route, requests, aligned CPU and GC deltas, window bounds), per-run RSS, CPU, GC totals, decoded body hashes and log volumes are in `raw.json`; binary hashes, seed hash and host identity in `metadata.json`; the GC and startup lines of each server run in `*.log`. The per-request access log is left on (harness fidelity) and only its line and byte counts are kept. This is a workstation measurement on a shared host; medians, spread and paired signs are the evidence, not single samples.

