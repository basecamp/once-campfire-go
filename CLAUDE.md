# Working on once-campfire-go (engine branch)

This fork adds a from-scratch performance engine to the validated Go port of
ONCE Campfire. Upstream's `AGENTS.md`, `plans/validation.md` and
`plans/go-conversion.md` still describe the application; this file carries the
working contract for the engine work.

## Core tenets: performance-aware programming

**These are the core tenets of this work. Read them before writing a line.**

The stance is Casey Muratori's: *performance-aware programming*. Not
"optimization" as a phase that happens after the code works — knowing roughly
what the machine will do with what you write, while you write it. The
alternative is not "clean code that gets optimized later"; it is code whose
shape forecloses the fast version, and the rewrite costs more than thinking for
five minutes did.

Two ideas underneath everything below:

- **Know the order of magnitude before you type.** How many times does this run
  — once, per request, per row, per element? What does one iteration touch?
- **The machine is not an abstract machine.** It has caches, a prefetcher, wide
  registers, and many cores. Code that pretends otherwise leaves 10-100x on the
  floor, and no amount of later profiling recovers a layout decision.

**How the tenets relate.** They are not a list of independent good ideas. The
data-layout ones exist to make the bulk operation POSSIBLE:

    struct-of-arrays + grouped lifetimes + zero per-element allocation
        -> contiguous, uniformly-typed arrays
            -> the kernel can run at all
                -> SIMD, and the parallel shard boundaries come free

### 1. Zero allocations wherever it is possible at all

Not "few" — zero, on any path that runs per element, per record, per row or per
request. Size every slice; reuse the caller's buffer; append into a supplied
`dst`; compact in place; do not scan twice; check escape analysis with
`go build -gcflags=-m`; prefer an index into a slab over a pointer chase.
Verify with `-benchmem`; `0 allocs/op` is a real target on hot paths.

### 2. Think about the data, then the code

Layout decides the speed. Struct-of-arrays for anything scanned columnwise;
group lifetimes so allocation boundaries match lifetime boundaries (per-request
arenas); use the whole cache line; keep hot fields adjacent. Locality is a
hypothesis checked with perf counters, not a rule applied blindly.

### 3. Do the work in bulk — use SIMD

Whole-slice work goes through the kernels, not a hand-written scalar loop.
This tree consumes `github.com/sebishogun/simd`, `.../simdhttp` and
`.../simdjson` where they are measured to help. Verify dispatch reaches the
kernel at runtime. A per-element function call defeats vectorization.

### 4. Don't do the work at all

Prune before you decode; hoist invariants; never scan twice. For this project
the headline corollary: **never recompress a page that has already been
compressed, never rehash a body that has cached part digests, and never query
what a version number can answer.**

### 5. Multi-threaded where it is beneficial

And only there. Shard on a boundary the data already has; private output
buffers; merge once. `-race` is a gate.

### 6. `sync.Pool` is the last resort — and it has to be correct

Reach for it last: size hints, arenas, caller-supplied buffers come first.
When a pool IS used: fully overwrite before reading, prove it with a poisoning
test FIRST, unambiguous ownership, put back exactly once, pool a pointer, and
state the sizing contract in the doc comment.

### Then measure

These tenets are where to start, not a substitute for the benchmark.
Fast-looking code that was never measured is a guess. Every change is judged by
the repository's benchmark contract (`plans/2026-10-05-engine-design.md` §6):
interleaved A/B in one session, minimum of repeated samples, quiet machine
(load < 1), `perf stat` instructions/cycles when the change is smaller than
wall-clock spread, and disassembly before theorising. A claim with no number
behind it does not go in a doc.

## Working rules

- **Correctness is the referee.** The engine must keep every validation in
  `plans/validation.md` green. Differential tests against the legacy path
  (`CAMPFIRE_ENGINE=off`) are written before engine code for a route.
- **Strangler only.** The engine owns an explicit route table; every other
  path delegates to the legacy `web.Server` untouched. No half-migrated route.
- **One task at a time** from `plans/2026-10-05-engine.md`, with its task ID
  named in the session. Gates: `go test ./...`, `go test -race ./...`,
  `go vet ./...`, `gofmt -l .` — bare, with explicit timeouts; a hung test
  binary is a leak alarm, not a retry candidate.
- **Never build or test without a timeout.** No build/test on a repeat loop.
- **The record.** Measurements that argue against a change go in
  `plans/wrong.md` whether or not code changed.

## Where things are

- Design: `plans/2026-10-05-engine-design.md` (this project).
- Task plan: `plans/2026-10-05-engine.md`.
- Upstream application record: `plans/go-conversion.md`, `plans/validation.md`.
- Benchmark harnesses: `bench/` (native A/B); the official production-image
  comparison lives in `../once-campfire-elixir/bench/` (to be cloned).
