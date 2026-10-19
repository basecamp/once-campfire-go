# ENGINE-47: `CAMPFIRE_GOGC` 100 vs 200 (one binary, read workload)

**Decision: the shipped image sets `CAMPFIRE_GOGC=200` (Dockerfile `ENV`); `CAMPFIRE_GOGC=100`
rolls it back. No `CAMPFIRE_GOMEMLIMIT` is shipped.** The decision is unchanged from the first
measurement, but the numbers are corrected: the per-request figures below are aligned-window
measurements, and the first version's were not (see [Correction](#correction-aligned-window-vs-whole-process-cpu--timed-requests)).

Reproduce with `bench/gc-policy`; the generated tables are in [`report.md`](report.md), the
per-window samples, per-run totals, binary hashes and log volumes in local-only `raw.json`, the
host and binary identity in local-only `metadata.json`, and the GC and startup lines of each
server run in `*.log`.

```sh
bin/build
bench/gc-policy --values 100 100@null 200 200:256MiB --reps 16 --warmup 2 --seconds 4 \
  --out bench/results/gc-policy-opt-runtime
```

## What was measured

The same binary in every arm; only `CAMPFIRE_GOGC` (and, in one arm, `CAMPFIRE_GOMEMLIMIT`)
differs. Revision `ce7c42f`; the binary was `./campfire`, the PGO build of that revision
(sha256 `c73832fb…`, 42,638,984 bytes, the profile in force at the time — recorded in
`metadata.json`). Go 1.27.1, `AMD RYZEN AI MAX+ 395`, 16 physical cores. The server is pinned to
CPUs 8-11 and the load generator to 12-15 (the shared harness's pinning), `GOMAXPROCS=4`, against
a disposable SQLite backup of `once-campfire-verification/fixtures/default`. Reads are room_show,
messages_page, sidebar and search at concurrency 16 through the public front listener, gzip
requested, the harness's 2 s warmup and a 4 s timed window per route. Sixteen repetitions per arm,
the arm order rotating cyclically so every arm occupies every slot. `GODEBUG=gctrace=1` is set in
every arm and the per-request access log is left on, so the workload is the shipped composition.
Host load at the start of the run: 1.6 (1 min) — a quiet window on this otherwise shared host.

`100@null` is a second, identically configured `GOGC=100` arm: the host's noise floor for the same
statistic. It is the yardstick, not a result.

Per-request figures are **aligned-window** measurements: the process's CPU time is read from
`/proc` immediately before and after the timed load-generator window, and each GC cycle is
attributed to the window its **start** timestamp falls in. Attribution is start-attributed and
whole-cycle over half-open windows `[start, end)`: Go's gctrace `@Ts` is the time the runtime
records when a cycle begins (sweep termination), not when it completes, and the clock origin is
the script's own post-`Popen` reading rather than the runtime's init time. So a cycle that begins
in a warmup counts there even if it finishes inside a measured window, and one that begins inside
a window counts there in full even if it finishes after it. Cycles are 0.1–1 ms against 4 s
windows, so the misattribution is bounded by the handful straddling each boundary; the boundary
behaviour is pinned by `bench/test_runtime_levers.py`. The `/proc` CPU deltas do not depend on
any of this. What the attribution does *not* measure is a cycle's completion time — the runtime
does not record one.

The decoded body of every route is fetched once per run, decoded, normalized for the listener
origin and the room page's live cursor, and is equal in all 64 runs. This is decoded-body equality
after that normalization — it is **not** raw-wire identity (byte-for-byte bodies, headers and
framing are not asserted here; the PGO/no-PGO binaries were separately checked for identical
decoded bytes on the same routes).

## Numbers (medians over 16 runs; ratios are paired within a repetition and route)

| Arm | CPU µs/req | GC CPU µs/req | GC cycles | GC share | RSS peak MiB | RSS final MiB |
|---|---:|---:|---:|---:|---:|---:|
| `100` | 23.84 | 1.645 | 5096 | 5% | 73.9 [71.6–78.4] | 69.8 |
| `null` (`GOGC=100`) | 23.82 | 1.632 | 5048 | 5% | 74.1 [71.4–77.6] | 69.6 |
| `200` | 22.66 | 0.842 | 2468 | 3% | 86.7 [82.6–92.8] | 80.0 |
| `200+256MiB` | 22.68 | 0.847 | 2476 | 3% | 84.3 [80.3–90.8] | 80.6 |

Pooled over all four routes and 16 repetitions (64 pairs per arm):

| Arm | req/s ratio vs `100`, median [min–max] | pairs >1 | CPU µs/req ratio, median [min–max] | pairs <1 | GC µs/req ratio, median [min–max] | pairs <1 |
|---|---:|---:|---:|---:|---:|---:|
| `null` | 1.001 [0.549–2.254] | 34/64 | 1.001 [0.812–1.197] | 31/64 | 0.996 [0.317–2.563] | 34/64 |
| `200` | 1.056 [0.516–2.447] | 57/64 | 0.957 [0.761–1.137] | 57/64 | 0.516 [0.166–1.474] | 60/64 |
| `200+256MiB` | 1.061 [0.919–2.416] | 59/64 | 0.955 [0.775–1.108] | 59/64 | 0.512 [0.160–0.617] | 64/64 |

## Correction: aligned window vs whole-process CPU / timed requests

The first version of `bench/gc-policy` divided the whole process's CPU time — and the whole log's
GC CPU — by the *timed* requests only. Startup, login/scrape, the fetch calls and every warmup
window were in the numerator and not in the denominator, so every per-request figure was inflated
by a factor that depended on how much warmup and setup work happened to run. On this host that
factor was **1.42×** for CPU and 1.46× for GC CPU, and it is nearly identical across arms, so the
ratios survived but the quoted µs/request did not. The figures above are the corrected ones:
`100` 33.92 → 23.84 µs/req and `200` 32.25 → 22.66 µs/req.

The correction is verified by varying the warmup duration with everything else fixed
(`bench/results/gc-policy-warmup-check/`, five repetitions, 3 s timed windows):

| Warmup | Arm | CPU µs/req, aligned | CPU µs/req, whole-run (biased) | bias | GC µs/req, aligned |
|---:|---|---:|---:|---:|---:|
| 1 s | `100` | 24.05 | 31.29 | 1.30× | 1.735 |
| 1 s | `200` | 23.16 | 29.57 | 1.28× | 0.921 |
| 5 s | `100` | 24.31 | 57.88 | 2.38× | 1.752 |
| 5 s | `200` | 23.32 | 54.44 | 2.33× | 0.921 |

Quintupling the warmup moves the whole-run figure by +85%/+84% — the accounting artefact — while
the aligned figure moves by +1.1%/+0.7%, and the `200`-vs-`100` aligned ratio is 0.963 at 1 s and
0.959 at 5 s. The aligned measurement is the one to quote.

## Reading it

* **GC work halves, unambiguously.** GC CPU per request falls 1.645 → 0.842 µs (pooled ratio
  0.516, 60/64 pairs; the `256MiB` arm 0.512, 64/64), GC cycles 5096 → 2468, and the runtime's own
  GC share 5% → 3%. The noise-floor arm sits at 0.996 (34/64) — that metric does not move when the
  GC target is set to the same value.
* **Total CPU per request falls 4.9%** (23.84 → 22.66 µs; pooled ratio 0.957, 57/64 pairs; the
  noise floor 1.001, 31/64). Less than the 48% drop in GC CPU because GC is ~7% of total CPU here.
  Per route the ratios are 0.935 (14/16), 0.954 (14/16), 0.968 (15/16) and 0.957 (14/16).
* **Throughput follows**, on this quiet run: +5.6% pooled (median ratio 1.056, 57/64), per route
  1.085/1.061/1.041/1.052 with 14–15/16 pairs, against a noise floor of 1.001. Earlier runs on a
  busier host could not separate this from the host's noise; this one can.
* **The memory cost is real and bounded.** Peak RSS rises 73.9 → 86.7 MiB (+17%) and final RSS
  69.8 → 80.0 MiB, tracking the larger heap goal.
* **`CAMPFIRE_GOMEMLIMIT=256MiB` changes nothing here** (`200+256MiB` vs `200`: CPU 22.68 vs
  22.66 µs, GC CPU 0.847 vs 0.842 µs, peak RSS 84.3 vs 86.7 MiB, all inside the arm-to-arm
  spread). It is non-binding at this peak, so shipping it would add a knob with no measured
  benefit and a real risk of collector thrash in memory-heavy workloads that this read benchmark
  does not exercise. It is left unset; the knob remains available.

## PGO scope

The GC comparison above ran on a single binary — `./campfire`, the PGO build — so it says nothing
about PGO itself. PGO on/off was measured separately with the same harness and accounting
(`bench/results/pgo-on-off/`, 12 repetitions, PGO build `f3f063bb…` vs the same source built with
`-pgo=off`, `d46650a9…`): pooled CPU µs/req ratio no-PGO/PGO **0.995** [0.837–1.195] with 29/48
pairs below 1, against a noise floor of 1.007 with 18/48. The two medians are indistinguishable
and the signs are split — but the 48 pairs come from 12 repetitions of 4 routes sharing one
server process and one host window, so they are not 48 independent samples and no confidence
bound is claimed. The honest statement is the narrow one: **no PGO benefit was detected at this
sample size on this host**, not that no benefit exists. The shipped build uses the committed
profile because that was the lever's deliverable and it is verified to engage and to produce
identical decoded bytes, not because a speedup was measured.

## Uncertainty

* The host is shared; this run happened in a quiet window (load 1.6) and its throughput sign is
  clear, but the same experiment on a busier window could not resolve it. The CPU µs/req and GC
  counters are the stable evidence; throughput is reported for completeness.
* The measured workload is warm-cache reads with the per-request access log on, matching the
  shared harness. Uploads, media processing, Cable fan-out at high client counts and cold caches
  are not represented, and a higher GOGC raises peak memory most where allocation bursts are
  largest.
* The committed PGO profile was collected from a binary built from this revision with PGO off
  (identified by its own build info in `cmd/campfire/default.pgo.provenance.json`), so the profile
  is a bootstrap profile: it does not encode the effect of PGO itself.
