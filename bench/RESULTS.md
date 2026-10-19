# Benchmark results

The [validation record](../plans/validation.md#final-measurement-2026-10-15-train-1b143bf-image-once-campfire-gofinal2)
and [PR report](../plans/pr-description.md) retain the final measurements and
limitations. No benchmarks were rerun for the artifact cleanup or the subsequent
Copilot review fixes; the figures below do not measure those later changes.

## Final reported medians (requests/second)

Shared harness `ec02deb`: c=16, three alternating rounds, eight-second samples,
warm single-viewer tmpfs databases. The validation record identifies Go
`1b143bf` and Rust `f9dca47`, with zero errors/invalid responses and 660,976
acknowledged writes verified.

| Route | Rust | Go (container) | Go/Rust | C (native) | Go (native) | Go/C |
|---|---:|---:|---:|---:|---:|---:|
| Room | 109,468 | 148,605 | 1.36x | 156,727 | 147,748 | 0.94x |
| Messages | 104,716 | 194,387 | 1.86x | 173,380 | 197,124 | 1.14x |
| Sidebar | 124,164 | 162,273 | 1.31x | 168,853 | 163,809 | 0.97x |
| Search | 126,215 | 201,560 | 1.60x | 170,000 | 206,442 | 1.21x |
| Post | 8,177 | 15,392 | 1.88x | 7,456 | 17,073 | 2.29x |

The native C comparison is a separate run (server CPUs 8–11, client 12–15);
its Go column must not be mixed with the container run. C wins room and sidebar.
The reported same-binary `GOSIMD=scalar` arm achieved 0.989–0.994 of SIMD
throughput. Three rounds offer limited replication; these are warm-cache
measurements, not general workload guarantees. PGO benefit was undetected.

## Local evidence and historical reports

Raw receipts are local-only, not downloadable repository links:

- `/tmp/opencode/matched-final/summary.json`
- `/tmp/opencode/native-bench-20261014-232931/summary.json`
- `/tmp/opencode/matched-scalar/summary.json`

The medians above match those receipts and the retained reports. Receipt
`source_revisions` metadata differs from the revisions attributed in the
validation record: the matched receipts record Rust `ccece30` and Go `029c943`
(dirty), and the native receipt records Go `63236fe`. The cleanup preserves
the published attribution; reconciling build provenance remains outside this
cleanup. The scalar receipt also records a different Go image digest, so
the same-binary attribution is the existing report's claim, not independently
established by these summary files.

All historical Markdown reports remain in [results/](results/), including
[native baseline](results/baseline-native-20261005/report.md),
[final native](results/final5-native-20261007/report.md),
[GC policy](results/gc-policy-opt-runtime/README.md),
[PGO](results/pgo-on-off/README.md), and
[checkpoint policy](results/checkpoint-policy-20261014/README.md).
Raw non-Markdown outputs added by this PR remain on the measurement machine under
`bench/results/`, now ignored rather than tracked. Inherited upstream artifacts
and benchmark source helpers remain tracked. Raw paths in historical reports
denote local evidence unless the target is an inherited tracked file.

Local cleanup inventory and verification: `tmp/benchmark-cleanup/` (ignored).
This removes raw outputs from the final tree, not from existing Git history.
