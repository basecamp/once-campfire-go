# Avoid the unused body DOM copy — 2026-10-16

API body rendering cloned the prepared DOM, ran attachment/gallery transforms, then
serialized and reparsed it into another owned DOM. Without attachment nodes, those
transforms do nothing. That branch now borrows the original only for serialization.
It still reparses into an owned normalization tree, sanitizes, then serializes.
Attachment/error paths retain their clone. No parser, sanitizer, escaping, protocol,
SQL, authorization, signing or cache/queue/GC policy is weakened or changed.

Baseline: local `c0449f2`, runtime-equivalent to published PR `bd604ab`. One candidate
was tested. The current large attachment-free API allocation profile attributes
17.5% of sampled bytes to `richtext.clone`; no claim treats cumulative profile shares
as wall-time savings. The candidate profile confirms the unused clone is gone.

## Measurements

Three sequential alternating complete `APIRecord` query/render pairs:

| Body | Baseline time | Candidate time | Allocated bytes, baseline → candidate |
|---|---:|---:|---:|
| 1,000-item unordered list, no attachments | 1,132.272 µs | 1,027.095 µs (−9.3%) | 1,297,640 → 1,020,015 (**−21.4%**) |
| 1,000-item ordered list, no attachments | 1,188.621 µs | 1,057.614 µs (−11.0%) | 1,312,790 → 1,035,079 (−21.2%) |
| 20-item ordered list, no attachments | 45.488 µs | 42.808 µs (−5.9%) | 34,688 → 28,555 (−17.7%) |
| Ordinary paragraph | 17.564 µs | 17.504 µs (flat) | 10,730 → 10,354 (−3.5%) |
| Twenty actual mention attachments | 569.212 µs | 567.381 µs (flat) | 674,083 → 673,531 (flat) |

The target passes the declared 5% time/15% allocated-byte gates; other cases remain
within the 5% time/byte regression limits. This is copy elimination, not a second
list-numbering or JSON-encoding variant.

Complete public-listener controls extend the shared seed with one canonical message
per independent room/fixture. The maintained HTTP client offers 250 identity GETs/s,
eight workers and timer pacing, two warm plus eight timed seconds. All **75,000
warm/timed GETs** succeed with complete wire bodies. Preflights compare exact normalized
JSON, identities/counts/links, empty cursor windows, list structure, independently
specified plaintext and twenty actual mention replacements. Fixture FTS text is
checked independently. Timed GETs are drained, not individually JSON-decoded.
Integrity, populations, seed FK exceptions and clean joined exits pass.

| Median joined server CPU, three alternating pairs | Baseline | Candidate | Change |
|---|---:|---:|---:|
| Large unordered, attachment-free | 7.345755 s | 6.603249 s | **−10.11%** |
| Large ordered, attachment-free | 7.318053 s | 6.785037 s | −7.28% |
| Short ordered, attachment-free | 2.307610 s | 1.949263 s | −15.53%, variable |
| Ordinary paragraph | 1.814679 s | 1.742692 s | −3.97%, variable |
| Twenty actual mention attachments | 5.467009 s | 5.170975 s | −5.41%, variable |

Every target pair improves. All median ratios pass the declared benefit/non-regression
gates. The attachment-bearing path is unchanged except for detection, and its query
cost is flat; do not interpret its variable HTTP median as proof of a general gain.
Its adverse final pair is retained. Three short pairs provide no confidence bounds;
small/control-case results vary more than the isolated query costs.

Joined `wait4` CPU includes startup/login/preflight/warmup/final checks/shutdown and
excludes client/fixture costs. Measurements use local **Apple M3 Max/macOS arm64/Go
1.27.1**, CGO/FTS5 and four runtime workers with unchanged defaults. No task-owned
agent, test, build or profiler overlaps uninstrumented timing; other host activity
is not exclusively controlled. These are attachment-free API-body work results,
not native Fedora capacity/latency, broad frontend or refreshed ranking evidence.
Do not compound these percentages with earlier experiments.

## Verification

- Complete race-enabled richtext, presentation and web suites pass, including the
  existing oracle/security/nested-content cases. New shared-Document checks cover
  repeated/concurrent `Content`, script exclusion from plaintext, real attachment
  replacement and unchanged subsequent `Display`. Sanitization must never mutate
  the borrowed original.
- `bin/check` passes formatting/assets, Python regressions, root vet/races and nested
  WebSocket races. Removing the temporary probe leaves a byte-identical measured/
  tested binary: `781226de65b5cac9c3a8eea9b217f13dd00c0f2bab19be3ace17cc29ea8bf745`.
- Existing Go/Rust message-lifecycle, pagination, membership and direct-ping HTTP/state
  workflows pass **8/8**. They are adjacent shared contracts, not direct JSON-feed
  parity; Go API tests and complete public JSON preflights cover that body consumer.
- Independent source review found no concrete aliasing, normalization, malformed/
  Trix/error, namespace, gallery or combined-field issue. `hasAttachments` covers
  every node transformed by attachment replacement, and gallery mutation requires
  attachment members. The review did not independently rerun checks/measurements.
  No fresh full browser/media-output certification or exhaustive audit is claimed.

Compressed measurements retain budgets, every repetition and adverse outcome,
source/seed/binary identities, profile-owner summaries and checks. Diagnostic probes
are not permanent suite additions. `manifest.json` hashes artifacts. Raw profiles,
runtime logs, generated data/credentials, complete fixture rosters and binaries stay
private. No schema, frontend, version, reference or media byte-processing change is made.
