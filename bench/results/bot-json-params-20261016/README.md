# Bot JSON write boundary correction — 2026-10-16

Bot routes bypassed the existing JSON parameter parser. Malformed JSON created
messages, and scalar attachment JSON updates replaced message bodies with literal
JSON instead of applying attachment-only attributes. The old executable reproduced
these failures; pinned Rust passed the focused contract.

Bot intake now reuses the existing bounded parser before message/boost dispatch,
while preserving the original bytes for the API's raw-body consumer. Null and
non-null scalar attachment parameters select different attribute sets as in the
reference. Empty named-field arguments explicitly disable those assignments, so an
empty JSON key cannot overwrite an attachment-only update. Browser null attachment
assignment still detaches; bot null selects raw body and retains an existing blob.
Create body/attachment presence is validated independently of attribute selection.
The shared parser now rejects nonempty whitespace-only JSON rather than silently
allowing a destructive blank update. Empty input retains reference behavior.

Verification:

- The permanent regression fails on the old handler for malformed creates/updates,
  invalid attachment updates and incorrect empty-attachment body selection. The
  final test checks statuses, rejected-create counts, committed bodies, FTS results,
  and retention of an existing attachment across a null parameter update.
- Independent source review caught an initial candidate's empty-attachment presence
  bypass and the shared parser's whitespace-only omission. Added checks reproduced
  both before correction; `review-regression.log.gz` records that unretained state.
  The parent fixed and verified them. This review is not independent execution.
- `bin/check` and the full web race suite pass. The final Go/Rust HTTP/state runs pass
  **8/8**: message lifecycle, pagination, attachment lifecycle and room membership.
  A temporary extension of message lifecycle verifies the demonstrated JSON cases
  against both actual binaries and reads committed rich-text rows. Complete response
  bodies are consumed, and successful JSON updates are decoded.
- Two initial probe setup errors were corrected before valid reference comparisons:
  the extension needed its own authenticated actor, and persisted body belongs to
  `action_text_rich_texts`, not a `messages.body` column. They are fixture errors,
  not product findings; private failed logs remain available. The subsequent valid
  baseline report retains Rust passing and old Go accepting malformed JSON with 201.

Baseline local `5c9090c` has the same runtime as published PR `50dcfb5`. Source changes
are limited to the HTTP boundary; authorization, persistence, signing, body/upload
limits and postcommit lifetimes remain intact. JSON now incurs the existing decoder's
validation/parameter work; no speedup or new latency/capacity claim is made. This is
not complete malformed/nested Rack or arbitrary attachment-type parity. No media
byte processing changes or fresh full browser/media certification are claimed.
Compact proof and diagnostic source are hashed in `manifest.json`. Runtime logs,
credentials, generated databases/storage, complete rosters and binaries stay private.
