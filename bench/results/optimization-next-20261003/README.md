# Further Go optimizations — 2026-10-03

This pass compares the published Go port at `9042f5eabcbc718c389e09ab35ceea36a4185958`
with three additional optimizations and the unchanged Rust release binary. No framework,
dependency version, SQLite durability setting, schema, frontend template or signing contract changed.

## Results

Median requests/sec at 16 clients; the before and after binaries were measured in the same rotating comparison.

| Workload | Published Go | New Go | Change | Rust |
|---|---:|---:|---:|---:|
| Room page | 10,175 | 15,218 | +49.6% | 27,535 |
| Message history | 21,445 | 21,369 | -0.4% | 31,139 |
| Sidebar | 24,423 | 24,658 | +1.0% | 38,303 |
| Search | 14,817 | 14,841 | +0.2% | 30,807 |
| Post message | 4,025 | 5,036 | +25.1% | 7,740 |

Room-page p99 latency improved from 5.69 to 4.14 ms; message-write p99 improved from 16.03 to 12.49 ms. Rust remains 1.81× faster on room pages and 1.54× faster on writes. The other measured routes stayed within about 1% of the published Go version.

All nine application runs passed: 45 HTTP samples with zero errors, 345,913 acknowledged writes verified in both messages and FTS, and nine thumbnails with identical bytes. Go read-response sizes matched the published binary. Memory after HTTP was essentially unchanged (136.6 versus 137.3 MiB Pss); the previous 10,000-client memory figures are not comparable to this HTTP-only sequence.

## Changes

- Cache the surrounding room-page HTML in the existing bounded fragment cache. The complete
  page data (user, account, room, origin, platform, permissions, flash and other fields) keys
  the entry; only message contents and the refresh timestamp are excluded and inserted fresh.
  Authorization and page-data queries still run on each request. Random internal markers avoid
  interpreting user content as an insertion point. Cache misses use the original template.
- Let rich-text callers request display HTML, editor HTML or mention IDs independently through
  the same processing functions. Message rendering stops building unused API/editor output;
  webhook and push recipient selection stops rendering HTML. Push plain text is computed only
  when there are actual subscribers. The full processor remains available for API output.
- Enable the existing go-sqlite3 driver's 64-entry prepared-statement cache on the single writer
  connection. This reuses transaction statements without caching results or authorization and
  preserves the existing WAL, synchronous setting, transactions and rollback behavior.

## Measurement

The `before/` CPU profiles recorded the published binary; `after/` records the first two
changes, and `write-cache/` records writes with all three. These ten-second profiles guided
the changes; the repeated application comparison is the performance result.

The [application report](application/report.md) compares all three binaries with three
repetitions, five-second HTTP samples, two-second warmups, 16 HTTP clients, identical disposable
seed copies and four application CPUs. With three applications their order rotates so each
runs first, second and third once. The scope is room pages, message history, sidebars, search,
message writes and one real thumbnail per application run. Cable throughput, other client
counts, TLS and public compression throughput were not remeasured in this pass.

```sh
bench/application --out bench/results/my-next-run \
  --apps go-before go rust --baseline-go /path/to/published-go-binary \
  --routes room_show messages_page sidebar search post_message \
  --concurrency 16 --cable-clients --upload-reps 1 --reps 3 --seconds 5
```

Raw samples, source/binary hashes, load averages, latency ranges and response contracts are
retained with the report. These are warm-read workload measurements on a workstation;
room-shell cache misses still execute the original template. They do not establish language-wide
performance or guarantee the same gain in a room with constantly changing page data.

## Verification

- [Formatting, vet and race tests](validation/check.txt), including database transaction/rollback
  checks and upstream WebSocket tests, passed.
- Focused rich-text paths are checked against the full processor across the existing oracle
  corpus. Room-shell tests compare exact bytes against the original template after changes to
  timestamps, messages, users, roles, names, flash, styles, origin, frame mode, invitations and
  stream tokens, including cache hits and oversized-entry bypass.
- [Browser workflows](validation/browser-retry.txt) and [Go/Rust upgrade interoperability](validation/upgrade.txt) passed.
  The first browser attempt completed its actions but reported a navigation AbortError while
  screen captures ran concurrently; the standalone rerun passed without a code change or mask.
  The [original failed log](validation/browser.txt) is retained.
- [Screen comparison](validation/screens-report.json): 77 targeted room/message/composer/realtime
  cells, with all 75 applicable screenshots and accessibility trees matching and no capture
  errors. The pre-existing strict DOM/network differences and deleted-room typing difference
  remain. [Binary hashes](validation/screens-metadata.json). No new masks or allowlists.
