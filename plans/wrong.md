# ENGINE-61: what did not work (or did not work as expected)

Task: close the sustained cable fan-out gap at high client counts
(1,000 clients: Go 520 vs Rust 552 recorded). Working hypothesis: the
per-broadcast per-client channel send and goroutine wake cost more than
tokio's broadcast waker.

## Profile evidence (first deliverable)

`tmp/engine-61/profiles/go-before-1000.pprof` (8 s cable, 1,000 clients)
and `go-before-1000-sat.pprof` (15 s saturated): top flat shares before
any change — `Syscall6` (writev) 42–45 %, `runtime.selectgo` 12–13 %
(writer's four-case park select), `websocket.(*mu).lock` 6 % (channel-based
write lock), `runtime_pollSetDeadline` 4–5 % (per-wake SetWriteDeadline
set+clear pair), `Hub.publish` 10 % (snapshot + authz + sends). The wake
path was real but NOT the biggest line; writev syscalls were. Server CPU
during saturation was ≈ 2.5 of 4 cores — not CPU-bound.

Findings that argue against the obvious fixes:

- **Per-wake SetWriteDeadline pair**: removing it (arm-only-when-stale,
  ENGINE-61) cut 4–5 % of samples and was kept. Cheap and safe.
- **The websocket `mu` channel lock**: tryLock fast path kept (~4 %).
- **Writer select → queued ring + cond** (per-client `outQueue`, heartbeat
  on its own goroutine): removed selectgo from the top of the profile and
  the total saturated-phase samples fell 41.75 s → 29.85 s for the same
  15 s window (about −30 % CPU), yet end-to-end delivered rose only
  ~520 → ~534 msg/s. The system is not server-CPU-bound; schedule- and
  receiver-side effects dominate the last few percent.

## The sustained-fan-out bottleneck is mostly the pinned load generator

Both servers saturate the Rust loadgen (4 CPUs pinned) during the
throughput phase; its CPU is ≈ 29.3 s per run for BOTH apps at the same
counts, and it runs at ~3.6 of 4 cores during saturation. Giving the
loadgen 8 extra CPUs lifts Go to 751 msg/s and Rust to 1,278 msg/s at
1,000 clients — i.e. Go's server has ~40 % headroom beyond the official
harness's measured ceiling, and the residual 2 % gap at the official pins
is the loadgen's per-frame receive cost, not server CPU.

## Writev coalescing was already happening

A temporary in-process counter (not committed) showed ~389k writev/s for
~517k delivered msg/s at 1,000 clients, i.e. ~2.7 frames per write: the
writer's drain already coalesces, driven by receiver backpressure. The
hypothesis that Go does one write per frame and Rust one per fan-out is
refuted.

## Benchmark pin sensitivity

Server on CPU 16-19 instead of the official 8-11 changes Go's 1,000-client
result by ~5 % (515 vs 534) with identical harness parameters; the second
worktree's benchmark on the official pins visibly corrupts measurements
(load 5–10 vs < 2). A/B pairs must be interleaved in one quiet window, as
was done for the ENGINE-61 numbers.

## Residual gap at the official pins (reported floor)

After the change, paired interleaved medians (official pins, deflate 0):
100 clients Go 5,084 vs Rust 4,261 (Go 1.19×, was 1.25×), 500 clients
1,085 vs 1,100 (0.99×, parity), 1,000 clients 534 vs 548 (0.98×, was
0.94×). Saturated p50 at 1,000 stays faster than Rust (13.9–14.0 ms vs
15.9–16.2 ms). The remaining ~2 % is attributed to the pinned loadgen's
receive efficiency (see above), not to any measured server-side cost;
deflate=1 at 1,000 shows 332 vs 370 (0.90×) and needs separate work.