# Verified follow-ups — 2026-10-16

Three scoped improvements follow the original PR head `e1be505`. Runtime changes
are limited to the placeholder query, response-local API creator presentation and
on-demand mention preparation. Existing SQL/storage/cookie/frontend formats,
authorization, signing, cancellation and queue/cache/GC policies remain unchanged.
No rejected autocomplete or scalar-JSON experiment is applied.

| Change and measured workload | Retained result |
|---|---|
| Stop placeholder exclusion discovery once its existing nineteen-slot limit is exhausted | Foreign-invalidated, 501-person/hidden-DM sidebar: **21.7% less complete server CPU**; ten-person case flat |
| Prepare each API creator once per response | Forty-message/single-author API page: **13.8% less complete server CPU**, **26.7% fewer query allocated bytes** |
| Sign/format captured mention values only when resolved | Forty-author API page without mentions: **11.6% less complete server CPU**, **33.8% fewer query allocated bytes** |

These results concern different workloads and sequential source baselines. **Do not
add or multiply the percentages.** They are local Apple M3 Max/macOS arm64 work
measurements using Go 1.27.1, CGO, SQLite FTS5 and four runtime workers—not refreshed
native Fedora latency/capacity, general POST or Go/Rust ranking claims. The original
PR's native comparisons describe its earlier source; these follow-ups do not
establish a repair of the reported POST-latency or Rust live-delivery deficits.

## Contracts and trade-offs

The placeholder cap stops only redundant exclusion discovery. At nineteen distinct
exclusions the existing output limit is zero. It does not cap real direct rooms or
displayed participants; invisible/inactive membership and the extra viewer-slot
behavior remain unchanged. The regression adds exact eighteen/nineteen boundaries.

API creator reuse lasts only for `APIMessages`. Each request still reads fresh,
snapshot-owned metadata. Empty pages retain `[]`, and one-record responses avoid
map allocation. The all-unique query pays about **0.7% time/0.9% bytes** overhead;
the one-record case is effectively flat. New tests protect mixed/repeated creators
and fresh name, role, avatar version and origin across calls.

Mention preparation captures user values immediately and uses standard-library
`sync.OnceValue` for concurrent first resolutions. Verified lookups still verify
every token; cached presentation cannot bypass signature, purpose, expiry or legacy
checks. The unverified User-only display exception remains intentional. The dense
mention query pays about **1% time/0.2% bytes** overhead; its complete HTTP CPU is
flat. Tests cover snapshots, concurrent use, errors, legacy nil-target lookup and
a tampered token after a valid user value has already been prepared.

## Measurement and verification

Each change has three sequential alternating baseline/candidate pairs. No task-owned
agent, test, build or profiler overlaps uninstrumented timing; other host activity
is not exclusively controlled. Three short pairs do not establish confidence bounds.
Adverse valid outcomes are retained: creator-reuse all-unique HTTP pairs vary,
including one about +5.8%, while its median passes the declared limit. Dense-mention
HTTP pairs also vary slightly in both directions. There is no universal speedup claim.

The sidebar control performs a genuine foreign settings commit before every GET,
exercising changed/cold work rather than ordinary warm-cache hits. All **72,012**
complete responses match origin-normalized body hashes within/across arms. The API
controls use the real cookie-authenticated JSON route, not bot-token capacity:
**60,000** creator-reuse and **90,000** mention-preparation warm/timed GETs succeed
and consume their full wire bodies. Complete preflights compare JSON/HTML, creator
identities, counts, links, single/empty pages and live foreign metadata changes.
Timed API GETs are drained, not individually JSON-decoded. Joined `wait4` CPU includes
startup/login/preflight/warmup/final checks/shutdown and excludes client/fixture work.
Database integrity, populations, original seed FK exceptions and clean exits pass.

The compressed measurements retain complete per-run server records, maintained
HTTP-client records where used, all query repetitions, budgets, exact seed/binary
identities and SQL VM/plan evidence. The placeholder Python VM-step probe uses
SQLite 3.53.1; actual Go timing is measured separately. Compressed scripts/probes are
diagnostic inputs, not a permanent benchmark suite. Source lineage labels distinguish
unpublished local experiment IDs from the patches actually applied to this PR.
Runtime logs, credentials/cookies, complete fixture rosters, databases and binaries
are excluded. `measurements-manifest.json` hashes each retained input/data artifact.

`verification.json` and `check.log.gz` record fresh verification of the combined PR
source. Existing Go API role/body/attachment/mention/pagination tests and richtext
oracle/security cases complement adjacent shared HTTP/state workflows, which do not
directly cover the JSON feed. No fresh full browser/media certification or exhaustive
correctness/security audit is claimed.
