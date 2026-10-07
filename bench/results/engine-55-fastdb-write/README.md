# ENGINE-55 — direct CGO write path for the create transaction

Interleaved A/B of `BenchmarkCreateMessageQueued` (internal/database/writer_test.go) on the
ENGINE-55 tree: direct fastdb lane (default, `CAMPFIRE_FASTDB_WRITE` on) vs the database/sql
lane (`CAMPFIRE_FASTDB_WRITE=off`, behaviour identical to the pre-ENGINE-55 tree). Machine:
AMD Ryzen AI MAX+ 395, quiet (load < 1). Command shape:
`taskset -c 16-31 nice -n 19 env GOMAXPROCS=4 go test -tags sqlite_fts5 -run '^$' -bench '^BenchmarkCreateMessageQueued$' -benchmem -benchtime=5s -count=1 ./internal/database/`,
3 interleaved rounds, medians below. Baseline (pre-ENGINE-55, same command): queued 39,811 ns/op,
4,112 B/op, 98 allocs/op; direct (queue off) 30,838 ns/op, 3,807 B/op, 95 allocs/op.

| round | fast lane | legacy lane |
|---|---|---|
| 1 | 32,733 ns/op, 971 B/op, 26 allocs/op | 39,438 ns/op, 3,895 B/op, 97 allocs/op |
| 2 | 32,221 ns/op, 971 B/op, 26 allocs/op | 39,613 ns/op, 3,895 B/op, 97 allocs/op |
| 3 | 32,182 ns/op, 971 B/op, 26 allocs/op | 39,786 ns/op, 3,896 B/op, 97 allocs/op |

Median: 32.2 µs/op vs 39.6 µs/op (1.23×); allocs/op 26 vs 97 (3.7×); B/op 971 vs 3,895 (4.0×).
`BenchmarkCreateMessageDirect` (queue off) is untouched by design: 30.6 µs/op, 94 allocs/op
(was 30.8 µs/op, 95).

CPU profiles (queued + direct benches, 5 s each): `runtime.cgocall` flat share of samples
73.0 % (9.90 s of 13.56 s, 331k ops total) before → 79.3 % (10.12 s of 12.76 s, 368k ops)
after. Per-op cgocall time 29.9 → 27.5 µs; per-op non-cgocall (driver glue, interface boxing,
allocations) 11.1 → 7.2 µs. The share rising is expected: the Go glue the crossings used to
drive is gone, so the (mostly irreducible) per-statement crossings dominate the remainder.
Profiles: `tmp/before-queued.prof` (baseline), `tmp/after-both.prof` (ENGINE-55).

Statement count on one POST /messages (counting harness): 12 with `CAMPFIRE_FASTDB_WRITE=off`,
5 with the default on — the create transaction's seven statements leave the counted driver;
the direct lane's own instrumented statement stream is pinned text-for-text in
`internal/database.fastwrite_test.go TestFastWriteStatementStream`.

Full suite: `go test -race -count=1 -timeout 800s -p 2 -tags sqlite_fts5 ./...` PASS (twice).