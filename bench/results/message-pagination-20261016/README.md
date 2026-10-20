# Message cursor input correction — 2026-10-16

HTML and bot JSON pagination read raw query values with `url.Values.Get` (first
scalar wins), bypassing normalized request parameters (last scalar wins). Failed
integer parsing and supplied zero also became the omitted-cursor sentinel, silently
selecting another page. API Link direction treated whitespace as a present value.

Both readers now share page selection and reference integer-prefix casting; bot
requests run the existing scalar normalizer. Authorization still precedes cursor
selection, and HTML cursor validation precedes response-cache lookup. Database
selection, strict timestamp boundaries, snapshots, cache identities and budgets are
unchanged. `before` page precedence and independent `after` Link precedence remain.
README records the corrected behavior. No speedup is claimed.

Verification:

- The extended existing API regression fails against the old handlers for repeated
  values, cleared/blank `after`, numeric prefixes, zero and invalid cursors. It passes
  the fix for both JSON IDs/Link metadata and HTML message inclusion/statuses.
- A focused parser table reuses reference Ruby cast vectors for signs, ASCII versus
  Unicode whitespace, decimal prefixes, underscores, NUL suffixes and int64 bounds.
  Supplied zero is rejected rather than becoming an omitted cursor.
- `bin/check` passes formatting/assets, Python regressions, vet and root/nested
  WebSocket races. No schema, frontend, dependency, signing or media change is made.
- Four existing Go/Rust HTTP/state workflows pass **8/8**: message lifecycle,
  pagination, room membership and direct ping. A temporary extension of the existing
  pagination workflow also checks the ten demonstrated scalar cases in HTML and
  JSON against both binaries, including full response consumption, decoded IDs,
  independently stored timestamps/counts and API Link policy. It is not a new broad
  acceptance matrix or a complete malformed Rack parameter certification.
- Independent read-only review found no actionable issue within this scalar-cursor
  scope. It checked current callers, reference casting and selection, authorization
  order and caching. The parent ran the checks; review is not independent execution.

`manifest.json` hashes compact proof and diagnostic source. Baseline is local
`48c26b8`, runtime-equivalent to published PR `82ee20a`; rejected allocation controls
remain separate and unshipped. Exact binary/seed/tool identities are retained in the
HTTP report. Runtime logs, generated databases/storage, credentials and binaries
remain private. No fresh browser/media certification or native performance claim
is made.
