# ENGINE-47 correction check: does the per-request figure move with the warmup?

The first version of `bench/gc-policy` divided whole-process CPU time (and whole-log GC CPU) by
the *timed* requests only, so warmup and setup work sat in the numerator and not in the
denominator. The corrected script reads the process CPU delta and the GC-cycle CPU of the timed
window itself. This directory is the check that the correction does what it claims: the same two
arms, the same 3 s timed windows, five repetitions, run once with a 1 s warmup and once with a
5 s warmup. If the accounting is right, the aligned figure barely moves when the warmup
quintuples, while the old whole-run figure grows with the extra warmup work it is counting.

```sh
bench/gc-policy --values 100 200 --reps 5 --warmup 1 --seconds 3 --out bench/results/gc-policy-warmup-check/warmup1
bench/gc-policy --values 100 200 --reps 5 --warmup 5 --seconds 3 --out bench/results/gc-policy-warmup-check/warmup5
```

| Warmup | Arm | CPU µs/req, aligned | CPU µs/req, whole-run (biased) | bias | GC µs/req, aligned | GC µs/req, whole-run (biased) |
|---:|---|---:|---:|---:|---:|---:|
| 1 s | `100` | 24.05 | 31.29 | 1.30× | 1.735 | 2.340 |
| 1 s | `200` | 23.16 | 29.57 | 1.28× | 0.921 | 1.185 |
| 5 s | `100` | 24.31 | 57.88 | 2.38× | 1.752 | 4.379 |
| 5 s | `200` | 23.32 | 54.44 | 2.33× | 0.921 | 2.197 |

* Aligned CPU µs/req: +1.1% (`100`) and +0.7% (`200`) across a 5× warmup. No systematic movement.
  (The aligned figures are start-attributed, whole-cycle measurements over half-open windows; see
  `bench/gc-policy`'s docstring and `bench/test_runtime_levers.py` for what that does and does not
  measure. The correction here is about the numerator/denominator, not the attribution.)
* Whole-run CPU µs/req: +85% (`100`) and +84% (`200`). The artefact, proportional to the warmup
  work it counts and unrelated to the timed requests.
* The `200`-vs-`100` aligned ratio is 0.963 at 1 s and 0.959 at 5 s, so the conclusion does not
  depend on the warmup either.

Generated tables are in [`warmup1/report.md`](warmup1/report.md) and
[`warmup5/report.md`](warmup5/report.md); per-window samples and per-run totals in the
`raw.json` files beside them. Both runs used `./campfire` (sha256 `f3f063bb…`). Host load was
2.7 (1 min) at the start of the 1 s run and 7.6 at the start of the 5 s run; this is a shared
host and the check is about the size of the accounting artefact, which is far larger than the
host's noise.
