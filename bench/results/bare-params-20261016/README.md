# Double-check: bare attachment nulls — 2026-10-16

An independent review of PR `a441e6f` caught a regression in bot attachment
selection: `?attachment` was flattened to an empty string and selected attachment-only
attributes, silently discarding nonempty raw message text. Rust treats the bare key
as null; `?attachment=` is a distinct, present empty string. A real bot-key regression
reproduced the lost body and missing FTS entry in the old Go handler.

Form intake now records scalar null metadata before `url.Values` loses it, and final
normalization applies query nulls with the same last-scalar/query-over-body precedence
as the values. Explicit values clear earlier nulls, including JSON body nulls. Bot
intake reuses the existing form parser; JSON syntax validation/raw bytes remain
unchanged. Ordinary value-only forms allocate no null metadata. Every normalizer
caller handles decoding errors. No SQL, cache budget, authority, signing, frontend,
media processing or postcommit-lifetime change is made; no speedup is claimed.

Verification:

- The permanent real-bot-key test fails the old source for bare query/form keys and
  overridden/repeated nulls. It checks creator identity, exact stored body and FTS
  membership, while explicit-empty controls remain distinct.
- A temporary extension of the maintained HTTP workflow creates the bot through the
  administrator endpoint and sends requests with no login cookie. Pinned Rust passes;
  the old Go executable loses the body. The corrected Go executable passes the same
  raw-body/FTS/source-precedence cases. Generated bot keys are redacted from traces.
- The final candidate passes `bin/check` and all fifteen existing Go/Rust HTTP/state
  workflows, **30/30**. The earlier double-check also passed all thirty on the broken
  head: those existing flows did not expose the missing bare-null distinction.
- The pristine shared Chromium smoke passes setup, two-tab live messaging/duplicate
  suppression/markup safety, permalinks, editing/search, profiles/accounts, QR, rooms,
  bots/styles, transfer/join and direct ping. The shared checkout had an unrelated
  three-line persisted-echo wait change; it was preserved, and a pristine `HEAD`
  export was used instead. Missing matching Playwright Chromium was installed locally.
- Actual PR-branch `bin/check` and all **30/30** HTTP/state workflows also pass.
  Its pristine browser rerun failed at the message-count assertion. That assertion
  does not wait for the sender's persisted echo; failure diagnostics later showed
  all three messages, but did not capture the failed instant's count. One bounded
  diagnostic run added the shared checkout's existing persisted-count wait (plus
  before/after logging), leaving the exact-three assertion unchanged, and completed
  the full flow. It observed three before/after, so it does not independently prove
  the original failure's cause. Both outcomes and the three-line harness-only diff
  are retained; no production or shared-checkout change was used to get this result.
- A fresh read-only review found no concrete issue in null ordering, current callers,
  multipart/nullable attributes or error/resource paths. Parent checks establish
  execution evidence; the review did not run tests.

Initial probe setup errors (cookie argument type, raw token versus the official
`id-token` bot key, and selecting a seeded room the new bot had not joined) were
corrected before valid comparisons. They are not product failures; private logs
are retained. The valid baseline report records Rust passing and old Go losing text.
Baseline local `0e65a86` has the runtime of published PR `a441e6f`. Compact proof,
reproduction source and hashes are retained here; runtime logs, tokens, generated
data/storage, complete rosters and binaries remain private. No full arbitrary Rack
parameter, browser-platform, media-output or whole-application correctness guarantee
is claimed. This double-check found and repaired a real gap; passing gates alone
had not established its absence.

## Browser-count follow-up

A bounded follow-up on runtime PR `41a8c2b` captured the assertion's state in one
browser evaluation. Three unforced instances produced two passes and one original
assertion failure: the sender had IDs 1/2 plus one pending message; the receiver had
IDs 1/2/3, with its third DOM ID matching the sender's pending ID. After rendering
completed, both had the same three unique persisted messages and no pending node.
The failed run remains a failure in the record; it was not relabeled a passing gate.

A fourth, controlled instance gated only the sender's third-message rendering and
reproduced that state, then converged after release. The original exact-three and
stored-markup assertions remain unchanged after synchronization. These are focused
phase diagnostics, not new full-browser acceptance runs. They establish a real
sender-synchronization gap in the harness; they do not retroactively capture the
older uninstrumented failed instant. In particular, request idle alone was not a
sufficient render-completion signal in the captured unforced failure.

The corrective wait belongs before the harness's exact-count assertion. Its existing
unrelated working-copy change was preserved, not edited or published by this task.
No production rendering workaround or cross-repository write was introduced. The
three-message diagnostic source, finite budget, all four outcomes and source/binary
identities are retained in the browser-count artifacts below.
