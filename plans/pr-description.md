<!-- PR body for basecamp/once-campfire-go. Numbers: FINAL5 official acceptance
     (tip 3305f44; the later review-fix commit is correctness-only). -->

# Performance engine: matches the published table's shape, beats Rust on every row

<!-- one-line summary: this branch adds a measured performance engine on top of the
     port — owned HTTP loop, direct SQLite read/write lanes, piece cache, cable wake
     path — with the same schema, storage keys, password hashes and Rails sessions. -->

## Results

Measured with 16 concurrent clients, fresh seed, production images, 3 rotating
reps, medians. Rails, Go and Rust re-measured here on the same machine with the
official harness; Django, Laravel, Express and Elixir figures are the published
numbers from this README's table (not re-measured).

| HTTP workload (requests/sec) | Rails | [Django](https://github.com/basecamp/once-campfire-django) | [Laravel](https://github.com/basecamp/once-campfire-laravel) | [Express](https://github.com/basecamp/once-campfire-express) | [Elixir](https://github.com/basecamp/once-campfire-elixir) | Go (this branch) | [Rust](https://github.com/basecamp/once-campfire-rust) | Go vs Rust |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Room page | 222 | 170 | 164 | 559 | 722 | **96,568** | 36,771 | **2.63×** |
| Messages page | 406 | 196 | 175 | 777 | 1,053 | **102,973** | 42,059 | **2.45×** |
| Sidebar | 539 | 615 | 715 | 4,125 | 1,275 | **60,081** | 35,269 | **1.70×** |
| Search | 382 | 315 | 305 | 1,294 | 1,156 | **132,259** | 33,951 | **3.90×** |
| Post a message | 269 | 154 | 137 | 256 | 801 | **8,502** | 6,772 | **1.26×** |

Go is ahead of Rust by 1.26×–3.90× on every published row, at roughly half the
CPU per request (38.3 vs 104 µs room page, 27.7 vs 97.5 µs search, 62.3 vs
108 µs sidebar, 35.5 vs 89.7 µs messages, 296 vs 378 µs post).

### Auxiliary routes

| Route (req/s at c=16) | Rails | Rust | Go (this branch) | Go vs Rust |
|---|---:|---:|---:|---:|
| `/up` | 4,176 | 242,009 | **453,789** | **1.88×** |
| Static CSS | 133,801 | 444,944 | **468,683** | **1.05×** |
| Avatar | 98,680 | 386,903 | **390,494** | **1.01×** |

### Action Cable fan-out

Max sustained msgs/s delivered to all clients, production images, 8 s windows:

| Clients | Rust | Go (this branch) | Go vs Rust |
|---:|---:|---:|---:|
| 100 | 4,043 | **5,196** | **1.29×** |
| 500 | 1,123 | 1,101 | parity (0.98×) |
| 1,000 | 542 | 538 | parity (0.99×) |

Saturated post→all p50 at 1,000 clients: 13.5 ms vs Rust's 16.1 ms.

## How

- **Owned HTTP/1.1 loop** (`internal/fastserve`, `CAMPFIRE_SERVER_LOOP=on`,
  default): vectorized request parsing, canonical-header fast path, precomposed
  response heads, one writev per response, recorded replay lane for cacheable
  GET/HEAD. TLS/ACME listeners stay on `net/http` + autocert unchanged.
- **Direct SQLite lanes** (`internal/fastdb`, `CAMPFIRE_FASTDB_WRITE=on`,
  default): CGO read lane with prepared-statement reuse and a write lane with
  group commit and checkpoints moved off the writer; read-mostly pages serve
  from memory-cached, version-keyed pieces.
- **Piece cache** (`internal/piececache`): per-message fragments, sidebar and
  whole re-renderable pages; pre-compressed pieces assembled into one
  browser-safe single-member gzip body.
- **Cable wake path** (`internal/cable`, `CAMPFIRE_CABLE_FAST=on`, default):
  subscriber index, exact-payload frame cache, per-client fixed-ring queue with
  a condition-variable writer park, batched vectored writes, generation-keyed
  authorization memo.
- **Routing/rendering**: compiled router and message renderer, arena
  allocations and precomposed framing on the hot path; every fast path has an
  `off` switch for A/B and rollback. (`CAMPFIRE_ENGINE` is a strangler
  scaffold that currently owns no routes — it is a pass-through; the measured
  wins come from the fast paths, not from route ownership.)

## Correctness

- Full `-race` suite (`bin/check`), browser workflow suite, and the screen
  parity inventory: **178/178 applicable cells match the reference** on pixels
  and accessibility (192 cells, 0 errors).
- Go↔Rust interoperability: Rust-issued sessions accepted by Go and vice versa;
  Rust renders and searches messages Go wrote (FTS).
- Container and ACME checks pass on the production image
  (`bin/check-container`, `bin/check-acme`).

## Measurement

Official harness (`once-campfire-elixir/bench/run`), production Docker images,
fresh seed, 3 rotating reps. Raw evidence and full history:
[`plans/validation.md`](plans/validation.md), with per-step profiles and
negative results in [`plans/wrong.md`](plans/wrong.md).

## Notes

- `github.com/sebishogun/simdhttp` and `github.com/sebishogun/simd` are
  MIT-licensed modules by this PR's author used only by the owned loop.
- Known deliberate differences from Rust (strict DOM/network specs, cache
  coherency scope, formatting) are listed under "Known differences" in the
  README.
