# Outbound response headers before inflation — 2026-10-16

Push delivery uses status headers, not response bodies. The old HTTP transport
initialized gzip/zlib before returning headers: an empty malformed compressed body
turned 201 into a delivery error and hid 404/410 from subscription invalidation.
A regression using the real decoder fails on all three old cases. A controlled
HTTP provider returning 410 with an absent gzip header also leaves the selected
subscription stored on the old source. Pinned Rust checks the push status without
reading the body (`integrations/web_push.rs:179–182`).

Inflation now begins on the first body read. Body consumers retain decoding errors;
an absent compressed header yields unexpected EOF rather than a successful empty
`io.ReadAll`. Only successfully initialized decoders are retained. Closing an unread
response does no body I/O; source-first closure unblocks a concurrent inflater-header
read before waiting for decoder cleanup. No provider policy, cryptography, delivery
budget, job admission or queue size changes.

Verification:

- Permanent tests cover status-only push handling through the real decoder, valid
  gzip/x-gzip/zlib content, malformed-body errors, unread closure and closure during
  blocked header initialization. Existing Webhook/OpenGraph oracle tests pass.
- The application regression uses a real controlled HTTP exchange and real decoding,
  changing only network routing. A compressed 410 deletes the selected subscription
  while retaining another user's subscription with the same endpoint. The old source
  fails both the delivery result and stored-row assertions.
- `bin/check` passes locally and on the desktop's Linux/amd64 with Go 1.27.1 and
  libvips 8.16.1. The desktop's dirty checkout was preserved; verification used an
  isolated worktree/container. Its cached toolchain initially lacked the `zstd` CLI,
  causing decoder tests to fail. A task-owned derived image adds that required tool;
  no host configuration or assertions were changed. Baseline and candidate then pass.
- The shared message lifecycle passes on Go/Rust (**2/2**). These ordinary HTTP checks
  complement, rather than exercise, the controlled provider regression.
- An independent read-only review found no actionable issue in the final design.
  Parent-owned runs establish execution evidence. A typed-nil decoder-close error in
  the first candidate was caught by the malformed-body tests and fixed before commit.

Baseline local `0b120a1` has the runtime of PR `8a0e6fa`. Source hashes match the Linux
candidate. Compact logs, toolchain identity and HTTP results are retained here; raw
runtime data, generated credentials/databases and binaries remain private. No external
push service was contacted, no new browser/media certification or performance claim
is made, and this is not proof of complete integration correctness.
