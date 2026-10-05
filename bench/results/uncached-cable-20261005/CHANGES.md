# Change log: uncached rendering, Cable fan-out, memory and latency — 2026-10-05

Follows [concurrency-20261004](../concurrency-20261004/CHANGES.md) and uses the same
environment and method: Docker Desktop (linuxkit 7.0, Apple M4 host), application on
CPUs 0–1 with 2 workers/readers each, the reference load generator on CPUs 2–3, 16 HTTP
clients, fresh copies of the parity seed. "Before" below is the code of
[#3](https://github.com/basecamp/once-campfire-go/pull/3). "Uncached" means
`CAMPFIRE_FRAGMENT_CACHE_MB=0` for both applications.

## 1. Autolinking skips text without URLs (`internal/richtext/autolink.go`)

The URL pattern is unanchored and case-insensitive over 26 schemes, so Go's regexp tried
it at every position of every message (20% of uncached room-page CPU). Every match
contains `://` or `www.` in some case; text with neither returns before the regexp runs.

- Plain message display: 41.7 → 14.3 µs. Messages with links: 55 → 51 µs.
- Tests: `TestURLPrefilterKeepsEveryMatch` (200,000 generated strings: every regexp match
  passes the prefilter); the 658-case Rust oracle.

## 2. Uncached messages render together with batched queries (`fragments.go`, `messages.go`, `storage.go`)

Each uncached message was rendered alone, with its own boost, attachment, room and creator
queries. They now render in one pass: boosts and attachments in one query each
(`BoostsByMessage`, `AttachedMany`; same per-message order), rooms and creators once per
page.

- Tests: `TestBoostsByMessageMatchesBoosts`, `TestAttachedManyMatchesAttached` (records
  with several, one and no attachments).

## 3. Message HTML from a compiled renderer (`internal/web/message_render.go`)

html/template spent 37 µs per message on reflection and escaper calls. The reference
compiles its templates; the message partial now has a renderer that writes the same
bytes, escaping each value as html/template does in that position (text and attribute
values; URL attributes filtered, normalized and escaped). The template remains the
definition and is still used for other message views.

- `BenchmarkMessageUncached`: template 29.0 µs and 420 allocations, renderer 3.2 µs and 11.
- Tests: `TestMessageRendererMatchesTemplate` renders 20,000 generated messages (every
  branch: emoji, attachments, 0–3 boosts; NUL, invalid UTF-8, quotes, `javascript:` and
  malformed URLs, extreme times and IDs) both ways and requires identical bytes;
  `TestURLAttributeMatchesTemplate` checks the URL escaping against html/template for
  every byte value. Deliberately breaking either escaper fails them.

## 4. Rich-text allocations (`richtext/dom.go`, `richtext/engine.go`, `html/token.go`, `web/presentation.go`)

- Escapers were built (`strings.NewReplacer`) on every call; they are built once.
- The HTML tokenizer allocated a 4 KiB buffer for every fragment; it now sizes the buffer
  to a string input (it still grows as needed). Allocation per plain message display:
  26 KB → 9.9 KB.
- Each user's signed avatar path is computed once and shared by the renderer and templates.
- Plain message display: 14.3 → 8.5 µs.

Uncached effect of 1–4 (req/s, interleaved): room 340 → 929, history 345 → 1,123, search
896 → 2,287, posting 5,255 → 7,099 (Rust 1,579, 1,602, 4,171, 6,613). Responses were
byte-identical to the previous binary with and without the cache after each change.

## 5. Action Cable writes (`third_party/websocket/prepared.go`, `internal/cable/hub.go`)

The server was not CPU-bound during fan-out (~108% of two CPUs): each frame larger than
the hijacked 4 KiB writer took two or three `write` calls, and every frame allocated a
timeout context and registered a context callback.

- Prepared frames are written with one vectored write of header and payload; frames
  already queued for a client are written together. Locking, close state, timeouts and
  errors follow the library's `writeFrame`; anything buffered falls back to it.
- Each connection has one watchdog timer (30 seconds per write, as before) and closes its
  socket once when the connection ends, instead of a timeout context per frame.
- Vectored writes alone made posting outrun delivery: 602 of 1,000 clients overflowed
  their 256-frame queues and were disconnected (the reference disconnects lagging
  clients the same way). Writing queued frames together lets a lagging client catch up.
- Tests: `TestPreparedFrameLengths` (every frame-length encoding, compressed and not,
  single and batched, interleaved with ordinary writes) and the upstream suite.

## Memory

With 1,000 Cable clients connected (no compression): Go 113 MB Pss, Rust 58 MB (both 40 MB
idle), about 73 KB vs 18 KB per client. Go's runtime statistics at that point: 2,017
goroutines with 24 MB of stacks (a reader and a writer goroutine per connection), 34 MB of
live heap (8 MB of it net/http's 4 KiB read and write buffers per hijacked connection),
14 MB freed but not yet returned. The goroutine-per-direction model of coder/websocket and
net/http's buffers set most of this; the reference's tasks are small state machines.
After the HTTP workloads Go uses ~134 MB vs ~101 MB, mostly GC headroom (`GOGC=100`).

## Latency

Go's p99 at 16 clients is about 2.3× Rust's while its p50 is lower. Measured causes:

- Not GC: with `GOGC=off` the tail did not shrink (room 3.4 → 2.4 ms, history 3.1 → 4.9 ms).
- Not cgo calls holding processors: `GOMAXPROCS=3` or `4` on the same two CPUs doubled p99.
- It grows with queueing: at 1 client Go's p99 matches Rust's (0.26–0.32 vs 0.17–0.29 ms);
  at 4 clients it is ~1.8×; at 16, ~2.3×. With both CPUs saturated, Go's scheduler spreads
  per-request waiting more widely than the reference's runtime.

## PGO

`cmd/campfire/default.pgo` was regenerated from this code with the same five workloads. Built
with the previous profile, sidebar and search used 2–4% more CPU per request than #3's build;
with the new profile all four read routes match #3 within run-to-run noise.
