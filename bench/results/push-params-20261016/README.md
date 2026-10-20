# Push registration parameter ownership — 2026-10-16

A new audit of push-registration intake found that its private JSON decoder ignored
normalized query parameters and merged nested JSON into an already populated outer
map. Six focused requests that Rust rejects with 400/422 instead returned 200 in Go
and stored subscription credentials. URL-encoded intake also merged body/query leaves
where the query should replace the entire subscription hash.

Push registration now consumes the typed request parameters owned by intake, as do
room attributes and direct uploads. JSON wrapping uses body parameters only and never
replaces an explicit body/query root. Strong attributes distinguish missing/blank
roots from permitted scalar values, preserving string/null credentials and casting
permitted booleans/numbers. The superseded JSON decoder and flat fallback readers
were removed; all callers of the renamed intake function were migrated.

A read-only review caught a related candidate defect before delivery: reconstructing
multipart trees with `url.Values.Encode` loses ordering across different field names.
It could persist three incorrectly grouped metadata hashes instead of two. Multipart
intake now retains the text-field sequence and uses the same decoded-key validator
and nesting builder as URL-encoded input. Query hashes still replace body hashes.
This adds at most 4096 pairs of string references, not another copy of field data;
existing part/file/text/body limits are unchanged. No SQL/schema, authentication,
provider delivery, cache budget, frontend or media-processing change is made, and
no performance improvement is claimed.

## Verification

- The permanent push regression fails the old source: each of the six rejected
  registrations writes a row. The existing deduplication/validation/deletion test
  remains. Additional affected cases cover multipart root replacement, canonical
  MIME handling and permitted scalar credentials.
- A temporary extension of the maintained account workflow confirms pinned Rust
  rejects the same requests without changing persisted subscriptions; the old Go
  executable returns 200 and writes a row. Final Go and Rust also agree on selected
  credentials, body-only JSON wrapping, signed-in-user ownership despite a different
  user ID in the route, and another user's inability to delete the subscription.
  Validation resolves a permitted service hostname; no provider notification is sent.
- The existing direct-upload metadata table now includes ordered multipart hashes
  and checks both response and SQLite metadata. This case fails a source overlay
  reproducing the review's intermediate sorted-map candidate, then passes the fix.
  A real Go/Rust HTTP probe independently checks the four-field/two-hash example.
- A focused parser regression checks that malformed structured fields still return
  cleanup for an earlier spooled file. Existing size-limit cleanup tests remain.
- `bin/check` passes on final local source. Eight affected existing Go/Rust workflows
  pass **16/16**: account settings, authentication, direct upload, upload metadata,
  room updates/membership, attachment lifecycle and avatar lifecycle. Only the account
  and metadata workflows are extended with the focused probes above.
- Removing obsolete fallback readers initially exposed a direct-handler test fixture
  that bypassed canonical request parsing. Its intake preparation was migrated; the
  stale-administrator rejection and unchanged-room assertions were not weakened.
  The failed check excerpt and intermediate ordering failure are retained.
- A second read-only review found no concrete issue in the ordered-capture correction,
  validation/depth, method selection, query precedence or cleanup. Reviews did not
  execute tests; parent-owned runs establish execution evidence.

Baseline local `d19a143` has the runtime of published PR `ea661a9`. Compact proof and
probe source are retained here; generated databases/storage, runtime logs, credentials
and binaries remain private. These checks do not establish complete Rack parameter,
provider, browser-platform, media-output or whole-application parity. No browser rerun
was needed for these server-side changes; the earlier disclosed message-count timing
failure and diagnostic outcomes remain unchanged, not recertified here.
