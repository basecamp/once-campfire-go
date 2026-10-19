# GC arms: aligned-window per-request accounting

`ce7c42f0d2b2879fb6e6c64a9e473fef30390c8d` (checkout dirty: True), Go 1.27.1, omarchy. Public front listener, gzip, 16 concurrent clients on 12-15, server pinned to 8-11, 5s warmup and 3s timed window per route, 5 repetitions per arm, order rotating cyclically each repetition so every arm occupies every slot. Decoded bodies are equal across every arm and run (normalized for the listener origin and the room page's live cursor). GODEBUG=gctrace=1 in every arm; host load at start: 7.62 5.50 5.72 3/3746 1204636.

Arms: `100` (GOGC=100), `200` (GOGC=200). An arm with the same settings as the baseline and a different label is the host noise floor. Binaries, with their hashes, are in `metadata.json`.

Every CPU and GC per-request figure below is an aligned-window measurement: the process CPU delta and the GC-cycle CPU of the timed load-generator window only, divided by that window's requests. GC attribution is **start-attributed and whole-cycle** — the gctrace `@Ts` field is the cycle's start, not its completion, and windows are half-open `[start, end)` — so a cycle beginning in a warmup counts there even if it finishes inside a window, and one beginning in a window counts there in full even if it finishes after. See the correction section at the end.

| Route | Arm | req/s, median [min–max] | CPU µs/req, median [min–max] | GC CPU µs/req, median | GC cycles, median | RSS peak MiB, median [min–max] |
|---|---:|---:|---:|---:|---:|---:|
| room_show | 100 | 129,753 [128,140–132,700] | 28.00 [27.55–28.64] | 2.352 | 815 | 73.8 [73.3–76.9] |
| room_show | 200 | 145,543 [141,810–146,704] | 25.58 [25.40–26.28] | 1.155 | 423 | 90.4 [84.0–90.7] |
| messages_page | 100 | 149,559 [147,754–151,617] | 24.50 [24.29–24.81] | 1.817 | 720 | 73.8 [73.3–76.9] |
| messages_page | 200 | 158,757 [148,643–160,259] | 23.41 [23.21–25.00] | 0.941 | 354 | 90.4 [84.0–90.7] |
| sidebar | 100 | 156,268 [154,950–157,406] | 22.42 [22.33–22.50] | 1.482 | 614 | 73.8 [73.3–76.9] |
| sidebar | 200 | 162,379 [156,645–165,283] | 21.76 [21.34–22.70] | 0.788 | 295 | 90.4 [84.0–90.7] |
| search | 100 | 153,698 [150,722–159,167] | 23.90 [23.22–24.48] | 1.704 | 669 | 73.8 [73.3–76.9] |
| search | 200 | 162,120 [146,945–165,020] | 22.98 [22.54–25.45] | 0.901 | 322 | 90.4 [84.0–90.7] |

## Paired ratios against the baseline (same repetition and route)

Ratios to the `100` arm measured in the same repetition and on the same route, and the number of pairs that favour the arm. A throughput ratio above 1 (or a CPU µs/req ratio below 1) with nearly every pair agreeing is a clear sign; mixed signs mean the host noise is larger than the effect. The route pairs of one repetition share a server process and a host window, so they are not independent repetitions: the per-route columns and the pooled row should be read together.

| Route | Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|---:|
| room_show | 200 | 1.125 [1.069–1.131] | 5/5 | 0.913 [0.897–0.954] | 5/5 | 0.491 | 5/5 |
| messages_page | 200 | 1.057 [0.995–1.083] | 4/5 | 0.955 [0.936–1.020] | 4/5 | 0.518 | 5/5 |
| sidebar | 200 | 1.033 [1.011–1.058] | 5/5 | 0.974 [0.950–1.009] | 4/5 | 0.533 | 5/5 |
| search | 200 | 1.067 [0.959–1.076] | 3/5 | 0.945 [0.939–1.064] | 3/5 | 0.524 | 5/5 |

Pooled over all routes and repetitions (all pairs, not independent):

| Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median [min–max] | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|
| 200 | 1.059 [0.959–1.131] | 17/20 | 0.954 [0.897–1.064] | 16/20 | 0.521 [0.481–0.576] | 20/20 |

## Correction: aligned window vs whole-process CPU / timed requests

The first version of this script divided the whole process's CPU time (and the whole log's GC CPU) by the timed requests only, so the warmup windows and the setup requests were in the numerator and not in the denominator. The aligned figure is the one to quote; the whole-run figure is shown to make the size of the correction visible. RSS and the GC cycle counts are unaffected.

| Arm | CPU µs/req, aligned | CPU µs/req, whole-run (biased) | bias | GC µs/req, aligned | GC µs/req, whole-run (biased) | bias |
|---|---:|---:|---:|---:|---:|---:|
| 100 | 24.31 | 57.88 | 2.38× | 1.752 | 4.379 | 2.50× |
| 200 | 23.32 | 54.44 | 2.33× | 0.921 | 2.197 | 2.39× |

Per-window samples (route, requests, aligned CPU and GC deltas, window bounds), per-run RSS, CPU, GC totals, decoded body hashes and log volumes are in `raw.json`; binary hashes, seed hash and host identity in `metadata.json`; the GC and startup lines of each server run in `*.log`. The per-request access log is left on (harness fidelity) and only its line and byte counts are kept. This is a workstation measurement on a shared host; medians, spread and paired signs are the evidence, not single samples.

