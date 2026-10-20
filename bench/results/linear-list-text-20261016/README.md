# Linear list numbering — 2026-10-16

Ordered-list plaintext previously rescanned every preceding element sibling for
each item, making ordinal calculation quadratic. It also rediscovered list ancestors
inside recursion. The renderer now carries nearest list kind, depth and element
ordinal through traversal. The subtree entrypoint derives its inherited context once.
Other output-copying costs remain; this is not a claim that every plaintext operation
is linear for arbitrary nested input.

Baseline: local `bab6f93`, runtime-equivalent to published PR head `64dbedc`. One
candidate was tested. Parser, sanitizer, serialized HTML, signing, authorization,
cache/queue/GC settings and plaintext/FTS formatting remain unchanged.

## Measurements

Three sequential alternating application-query pairs render complete captured
`APIRecord` responses with real SQLite metadata reads:

| Body | Baseline time | Candidate time | Allocated bytes, baseline → candidate |
|---|---:|---:|---:|
| 1,000-item ordered list | 2,233.184 µs | 1,177.569 µs (**−47.3%**) | 1,313,072 → 1,313,502 (flat) |
| 20-item ordered list | 45.755 µs | 45.396 µs (flat) | 34,685 → 34,679 (flat) |
| 1,000-item unordered list | 1,129.859 µs | 1,137.297 µs (+0.7%) | 1,298,017 → 1,297,807 (flat) |
| Ordinary paragraph | 17.484 µs | 17.385 µs (flat) | 10,722 → 10,724 (flat) |

The target passes its 10% query-time gate; other query time/byte medians stay within
5%. This removes pointer-walking work, not allocations. The baseline diagnostic CPU
capture unintentionally includes both ordered and unordered subbenchmarks because
Go matched the unanchored subbenchmark name. It is retained, not recaptured; native/
scheduler attribution is limited and no comparative savings claim uses that profile.
Controlled query logs identify each case explicitly. The optional candidate capture
uses the fully anchored ordered-list name.

Complete public JSON controls use four independent extensions of the shared seed,
each with one canonical message in a new room. The maintained HTTP client offers
250 identity GETs/s with eight workers and timer pacing, two warm seconds and eight
timed seconds per process. All **60,000 warm/timed GETs** succeed with complete wire
bodies consumed. Preflights compare exact normalized JSON, message identity, count,
links, empty cursor windows and list HTML. Plaintext is checked against an independent
numbered/bulleted-line reference; stored fixture FTS text matches it exactly. Timed
GETs are fully drained, not individually JSON-decoded.

| Median joined server CPU, three alternating pairs | Baseline | Candidate | Change |
|---|---:|---:|---:|
| 1,000-item ordered list | 8.751614 s | 7.878939 s | **−9.97%** |
| 20-item ordered list | 2.941183 s | 2.934034 s | −0.24%, flat |
| 1,000-item unordered list | 7.870917 s | 7.864196 s | −0.09%, flat |
| Ordinary paragraph | 2.068491 s | 1.993074 s | −3.65%, variable |

Each target pair improves. Other median ratios pass the declared 5% limit, but
paragraph results are mixed: two paired runs lose, including one about +13.7%.
They are retained. Do not interpret its median as a demonstrated paragraph gain
or a guarantee of no individual regression. Three short pairs provide no confidence
bounds. Joined `wait4` CPU includes startup/login/preflight/warmup/final checks and
shutdown; client and fixture creation CPU is excluded. Integrity, populations,
original seed FK exceptions and clean exits pass.

These are local **Apple M3 Max/macOS arm64/Go 1.27.1**, CGO/FTS5 work measurements,
with four runtime workers and unchanged production settings. No task-owned agent,
test, build or profiler overlaps uninstrumented timing; other host activity is not
exclusively controlled. Results concern unusually long numbered-list bodies, not
native Fedora capacity/latency, general chat performance or refreshed rankings.

## Verification

- Existing richtext oracle/security cases and complete richtext/database/web race
  suites pass. New exact cases protect nested lists, numbering that counts non-LI
  element siblings, ignored text/comments and inherited subtree ordinal/depth.
  The same expectations pass the original implementation through a source overlay.
- A focused real SQLite write probe creates a 1,000-item list, updates it to twenty
  items and checks exact committed FTS text against independently generated lines.
  This is diagnostic verification, not a permanent duplicate database test.
- `bin/check` passes formatting/assets, Python regressions, root vet/races and nested
  WebSocket races. Removing the temporary probe leaves an identical measured/tested
  executable: `29a12e6fb4819e22b999ac54129bfacac40b1f0b6f14e62aa9b7741d1acf419b`.
- Existing Go/Rust message-lifecycle, pagination, membership and direct-ping HTTP/state
  workflows pass **8/8**. Independent source review found no material semantic or
  complexity regression in Document/API, OpenGraph subtree or FTS callers. It did
  not independently rerun measurements. No full browser/media certification or
  exhaustive correctness claim is made.

Compressed measurements/probes retain budgets, all repetitions and adverse outcomes,
source/binary/seed identities and checks. The initial diagnostic fixture builder's
room-name type mismatch was corrected before any measurements. `manifest.json`
hashes retained artifacts. Raw profiles, runtime logs, generated data/credentials,
complete fixture rosters and compiled binaries stay private. No schema, frontend,
version, reference or policy changes are made.
