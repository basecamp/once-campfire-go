# Copilot review fixes implementation plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Address the five open PR #10 review threads, push the changes, reply in each thread, resolve addressed conversations, and request Copilot again.

**Architecture:** Keep the current routing design. Make listener closure idempotent with `sync.Once`; refresh webhook socket read deadlines when a request acquires its connection, while retaining per-read body deadlines. Reset benchmark scratch storage per iteration and test push-response independence with a blocked resolver.

**Tech Stack:** Go 1.27.1, `net/http`, `httptrace`, SQLite FTS5, GitHub CLI and GraphQL.

## Constraints

- No rebenchmarking or new throughput claims.
- No assistant signature or co-author trailer in commits or replies.
- Preserve local raw benchmark files and their ignore rules.
- Never run tests/builds without explicit timeouts; no retry/watch loops.
- Do not modify `reference/` or unrelated changes.

## Tasks

1. Add concurrent-close and HTTP/TLS webhook-reuse regressions. Run them with `CGO_ENABLED=1 timeout 120 mise exec go@1.27.1 -- go test -race -tags sqlite_fts5 -timeout 30s -run 'TestQueueListenerConcurrentClose|TestWebhookReadDeadline' ./internal/fastserve ./internal/integrations` and retain expected failures locally.
2. Add listener-owned `sync.Once`; refresh webhook read deadlines through a request-local `GotConn` trace composed with existing hooks. Test reused connections and stalled response bodies.
3. Add an arena reset inside the URL autolink benchmark loop; do not execute benchmarks. Strengthen `TestPushDeliveryRunsAsJob` so the POST must finish while its resolver is blocked, with bounded waits and cleanup.
4. Clarify scaffold comments and document `CAMPFIRE_COMPILED_ROUTES`; keep existing pass-through and compiled-router regression coverage.
5. Run formatting, vet, root race tests, nested WebSocket race tests and runtime-lever checks with explicit timeouts. Review the diff, then commit without attribution trailers and fast-forward push to `fork`'s `engine` branch.
6. Reply to each of the five open review comments with the pushed commit and applicable evidence; resolve only those addressed threads. Request a new Copilot review and verify GitHub state.

Local evidence: ignored `tmp/copilot-review-20261016/`. The pre-fix probes reproduced the listener panic, reused-request timeout, and arena accumulation; they are diagnostic tests, not throughput measurements.

## Subsequent Upgrade-handoff review

Review `5551241668` adds a new finding after the original five threads were
resolved. A pipelined Upgrade can leave its first frame byte in the disconnect
observer while its headers are already buffered. The handoff must join the
observer, append that saved byte after the buffered prefix, and clear ownership.

Add a deterministic GET-to-Upgrade regression with the observer holding the
first frame byte. Also test releasing the replay buffer after consumption and
preserving it across zero-length reads. Observe failures first, fix only this
handoff/replay path, then run bounded correctness gates. Push, reply to the new
thread, and resolve it only after the fix is present remotely. Do not rerun
benchmarks or add attribution trailers. Copilot review requests require an
upstream maintainer because the authenticated account has READ permission.

## Concurrent batch-write contract (2026-10-19)

The current hub's single batch writer does not make the library methods
concurrent-safe. Shared payload/Rsv1 scratch is modified before `writeFrameMu`
is acquired, and deferred scratch cleanup runs after the mutex is released.
This violates `Conn`'s concurrency contract even though the hub avoids the race.

Add a deterministic blocked-writer/cancelled-contender test and a real
concurrent-writer wire-integrity test covering all batch entry points, normal
prepared writes, and compression. Observe failures first. Move the existing
frame lock to cover preparation, writing and cleanup; do not add another lock,
allocation, or scratch copy per batch. Update the obsolete single-writer safety
comment. Run bounded correctness gates, push, and reply to the Copilot review
summary with the fix. No benchmarks or assistant attribution.

## Pooled zstd native-memory lifetime (2026-10-19)

Pooled compressors finish frames with `End`, not `Close`. GC can discard a
`sync.Pool` entry and oversized buffers are deliberately not returned, leaving
their C contexts without a reclamation path. Separate native ownership from
the writer/output cycle, with idempotent release shared by explicit Close and
an unreachable-writer cleanup. Use Go 1.27.1 `runtime.AddCleanup`, not a finalizer
that can retain a cycle; keep the writer alive across native calls.

Add bounded lifecycle tests for abandoned writers, dropped cyclic pool entries,
explicit/error Close, and reachable End/Reset reuse. Establish reclamation tests
fail before cleanup registration, then enable cleanup and rerun correctness
gates. Cleanup must only free native memory, never finish a frame or write to an
abandoned output. No per-Write/Reset cleanup registration and no benchmarks.
