# GC arms: aligned-window per-request accounting

`ce7c42f0d2b2879fb6e6c64a9e473fef30390c8d` (checkout dirty: True), Go 1.27.1, omarchy. Public front listener, gzip, 16 concurrent clients on 12-15, server pinned to 8-11, 2s warmup and 4s timed window per route, 12 repetitions per arm, order rotating cyclically each repetition so every arm occupies every slot. Decoded bodies are equal across every arm and run (normalized for the listener origin and the room page's live cursor). GODEBUG=gctrace=1 in every arm; host load at start: 6.15 5.79 5.75 5/3656 1214272.

Arms: `pgo` (go/GOGC=200), `pgo-null` (go/GOGC=200), `nopgo` (nopgo/GOGC=200). An arm with the same settings as the baseline and a different label is the host noise floor. Binaries, with their hashes, are in `metadata.json`.

Every CPU and GC per-request figure below is an aligned-window measurement: the process CPU delta and the GC-cycle CPU of the timed load-generator window only, divided by that window's requests. GC attribution is **start-attributed and whole-cycle** — the gctrace `@Ts` field is the cycle's start, not its completion, and windows are half-open `[start, end)` — so a cycle beginning in a warmup counts there even if it finishes inside a window, and one beginning in a window counts there in full even if it finishes after. See the correction section at the end.

| Route | Arm | req/s, median [min–max] | CPU µs/req, median [min–max] | GC CPU µs/req, median | GC cycles, median | RSS peak MiB, median [min–max] |
|---|---:|---:|---:|---:|---:|---:|
| room_show | pgo | 142,950 [66,943–147,177] | 25.89 [25.29–30.58] | 1.188 | 550 | 86.4 [84.1–90.6] |
| room_show | pgo-null | 141,900 [130,683–147,871] | 26.14 [25.10–28.54] | 1.193 | 550 | 87.7 [82.8–92.8] |
| room_show | nopgo | 146,136 [139,261–148,637] | 25.54 [25.09–26.78] | 1.099 | 507 | 82.5 [79.9–93.0] |
| messages_page | pgo | 157,721 [118,417–161,989] | 23.55 [22.99–25.43] | 0.945 | 465 | 86.4 [84.1–90.6] |
| messages_page | pgo-null | 155,734 [139,012–162,753] | 23.91 [22.92–26.81] | 0.956 | 464 | 87.7 [82.8–92.8] |
| messages_page | nopgo | 160,723 [75,340–165,686] | 23.24 [22.59–27.01] | 0.865 | 420 | 82.5 [79.9–93.0] |
| sidebar | pgo | 163,516 [148,441–165,908] | 21.62 [21.20–23.21] | 0.770 | 392 | 86.4 [84.1–90.6] |
| sidebar | pgo-null | 156,785 [143,061–168,303] | 22.52 [21.09–24.29] | 0.804 | 376 | 87.7 [82.8–92.8] |
| sidebar | nopgo | 162,273 [82,029–165,149] | 21.84 [21.49–25.69] | 0.792 | 388 | 82.5 [79.9–93.0] |
| search | pgo | 163,494 [158,666–165,396] | 22.75 [22.48–23.33] | 0.881 | 438 | 86.4 [84.1–90.6] |
| search | pgo-null | 162,064 [150,906–167,165] | 22.94 [22.28–24.65] | 0.884 | 440 | 87.7 [82.8–92.8] |
| search | nopgo | 164,181 [90,437–169,393] | 22.70 [22.02–25.37] | 0.803 | 383 | 82.5 [79.9–93.0] |

## Paired ratios against the baseline (same repetition and route)

Ratios to the `pgo` arm measured in the same repetition and on the same route, and the number of pairs that favour the arm. A throughput ratio above 1 (or a CPU µs/req ratio below 1) with nearly every pair agreeing is a clear sign; mixed signs mean the host noise is larger than the effect. The route pairs of one repetition share a server process and a host window, so they are not independent repetitions: the per-route columns and the pooled row should be read together.

| Route | Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|---:|
| room_show | pgo-null | 0.989 [0.947–2.209] | 3/12 | 1.009 [0.821–1.059] | 3/12 | 1.012 | 5/12 |
| room_show | nopgo | 1.029 [0.969–2.168] | 10/12 | 0.977 [0.837–1.038] | 9/12 | 0.909 | 11/12 |
| messages_page | pgo-null | 1.000 [0.878–1.374] | 6/12 | 1.001 [0.934–1.138] | 6/12 | 1.013 | 5/12 |
| messages_page | nopgo | 1.015 [0.636–1.102] | 10/12 | 0.986 [0.915–1.099] | 9/12 | 0.913 | 10/12 |
| sidebar | pgo-null | 0.978 [0.881–1.134] | 3/12 | 1.019 [0.909–1.135] | 3/12 | 1.012 | 5/12 |
| sidebar | nopgo | 0.995 [0.496–1.069] | 4/12 | 1.008 [0.951–1.195] | 5/12 | 1.030 | 3/12 |
| search | pgo-null | 0.997 [0.923–1.028] | 6/12 | 1.003 [0.974–1.084] | 6/12 | 1.015 | 5/12 |
| search | nopgo | 1.006 [0.549–1.036] | 8/12 | 0.999 [0.964–1.126] | 6/12 | 0.909 | 11/12 |

Pooled over all routes and repetitions (all pairs, not independent):

| Arm | req/s ratio, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median [min–max] | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|
| pgo-null | 0.990 [0.878–2.209] | 18/48 | 1.007 [0.821–1.138] | 18/48 | 1.012 [0.335–1.248] | 20/48 |
| nopgo | 1.008 [0.496–2.168] | 32/48 | 0.995 [0.837–1.195] | 29/48 | 0.925 [0.335–3.013] | 35/48 |

## Correction: aligned window vs whole-process CPU / timed requests

The first version of this script divided the whole process's CPU time (and the whole log's GC CPU) by the timed requests only, so the warmup windows and the setup requests were in the numerator and not in the denominator. The aligned figure is the one to quote; the whole-run figure is shown to make the size of the correction visible. RSS and the GC cycle counts are unaffected.

| Arm | CPU µs/req, aligned | CPU µs/req, whole-run (biased) | bias | GC µs/req, aligned | GC µs/req, whole-run (biased) | bias |
|---|---:|---:|---:|---:|---:|---:|
| pgo | 23.06 | 32.59 | 1.41× | 0.904 | 1.317 | 1.46× |
| pgo-null | 23.70 | 33.61 | 1.42× | 0.931 | 1.367 | 1.47× |
| nopgo | 23.22 | 32.41 | 1.40× | 0.858 | 1.240 | 1.44× |

Per-window samples (route, requests, aligned CPU and GC deltas, window bounds), per-run RSS, CPU, GC totals, decoded body hashes and log volumes are in `raw.json`; binary hashes, seed hash and host identity in `metadata.json`; the GC and startup lines of each server run in `*.log`. The per-request access log is left on (harness fidelity) and only its line and byte counts are kept. This is a workstation measurement on a shared host; medians, spread and paired signs are the evidence, not single samples.

