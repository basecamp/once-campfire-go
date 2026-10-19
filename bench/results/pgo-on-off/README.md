# ENGINE-47 scope check: PGO on vs off

The GC decision was made on one binary, so it says nothing about whether profile-guided
optimization itself helps. This directory measures that separately with the same harness, the
same aligned-window accounting and the same request windows.

```sh
go build -tags sqlite_fts5 -trimpath -pgo=off  -o .cache/pgo/campfire-nopgo ./cmd/campfire
bin/build                                              # ./campfire, -pgo=auto, the committed profile
bench/gc-policy --binary nopgo=.cache/pgo/campfire-nopgo \
  --values go/200@pgo go/200@pgo-null nopgo/200@nopgo \
  --reps 12 --warmup 2 --seconds 4 --out bench/results/pgo-on-off
```

| Arm | Binary | sha256 | CPU µs/req | GC CPU µs/req | RSS peak MiB |
|---|---|---|---:|---:|---:|
| `pgo` | `./campfire`, `-pgo=auto` | `f3f063bb…` | 23.06 | 0.904 | 86.4 |
| `pgo-null` | same binary again | `f3f063bb…` | 23.70 | 0.931 | 87.7 |
| `nopgo` | `-pgo=off` | `d46650a9…` | 23.22 | 0.858 | 82.5 |

Pooled over all four routes and 12 repetitions (48 pairs per arm), against the `pgo` arm:

| Arm | CPU µs/req ratio, median [min–max] | pairs <1 | req/s ratio, median | pairs >1 |
|---|---:|---:|---:|---:|
| `pgo-null` | 1.007 [0.821–1.138] | 18/48 | 0.996 | 18/48 |
| `nopgo` | 0.995 [0.837–1.195] | 29/48 | 1.011 | 32/48 |

**Result: no PGO benefit was detected.** The no-PGO binary's CPU per request ratio is 0.995 —
the same as the identically configured noise-floor arm's 1.007 — with signs split 29/48 and 18/48
against a fair 24/48. Throughput is likewise split (32/48 and 18/48). The 48 pairs are 12
repetitions of 4 routes that share one server process and one host window each, so they are not
48 independent samples: this establishes "no benefit detected at this sample size", not "no
benefit exists". A 1–2% effect would be invisible here.

So the shipped build's PGO input is a *mechanism* that is verified to engage (`go version -m`
reports `build -pgo=default.pgo`) and to leave the decoded response bytes unchanged, not a
measured speedup. The committed profile is a **bootstrap input**: it was collected from a
`-pgo=off` build of the same revision, so it does not encode the effect of PGO itself. The GC
default was therefore decided independently of PGO, on a PGO build of the same source.

Generated tables are in [`report.md`](report.md); per-window samples and per-run totals in
local-only `raw.json`; binary hashes and host identity in local-only `metadata.json`.
Host load at the start of the run: 6.2 (1 min), shared host, `GODEBUG=gctrace=1` in every arm,
per-request access log left on. The binary hashes above are the ones actually measured; the
committed profile was regenerated afterwards by a fixed `bin/pgo-profile`, so a rebuild now
produces a different PGO binary from the same source — this comparison is between a PGO build and
a no-PGO build, not between two fixed artifacts.

## Provenance of the committed profile

`cmd/campfire/default.pgo.provenance.json` identifies the binary that produced the profile from
that binary's own `go version -m` output — `binary.vcs_revision`, `binary.vcs_modified`,
`binary.module`, `binary.build_settings`, `binary.go_version_m` — rather than from the checkout,
and records the effective runtime settings from the server's startup log
(`effective.gc_percent`, `effective.mem_limit`, `effective.cpus_allowed`) alongside the requested
ones.

What it deliberately does **not** claim is the profile the binary was built with. `go version -m`
records the `-pgo` path and never the profile's content, so no file can be tied to the binary's
build from the binary alone. The file at that path is reported as
`pgo_input.candidate_before_run` / `candidate_after_run` with
`identity: "path only: … not proven to be the one it was built with"`, and the candidate is read
**before** the run writes anything, so a run using the default output path cannot record its own
output as its input (`pgo_input.candidate_overwritten_by_this_run` says whether it replaced the
file). Checked by building a binary with profile A (`f374b372…`), replacing the file with profile
B (`1300b8e3…`), and profiling that binary from this checkout: the provenance reports
`binary_setting = default.pgo`, the candidate's hash as B, the `path only` identity — and never B
as the build input. The same scenario under the previous version of this script recorded
`binary_pgo_input.sha256 = 1300b8e3…`, i.e. it named B as the input.

`source_identity` pins the checkout's build inputs by content (`go_sources_sha256`, 784 files —
the profile and its provenance are excluded as this tool's outputs, and so is `bin/pgo-profile`
itself, which does not go into the build) and states plainly whether they can stand for the
profiled binary. A VCS stamp names a revision and a modified flag, not content, so only a clean
build of the same revision matches; anything else is reported as
`checkout-only: not proven to describe the profiled binary` with the reasons. The same
cross-revision check above produced exactly that status, listing the revision mismatch, the
modified binary and the dirty checkout.
