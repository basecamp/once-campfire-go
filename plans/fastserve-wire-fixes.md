# fastserve wire fixes (2026-10-14)

Base: `48a2fbc`, branch `fix-fastserve`.

## Implemented

- Transport write failures are connection-sticky, including short writes with no
  error. Both vectored and raw writes remember the first error; later emissions
  are suppressed and return it. Finalization returns that failure to the loop,
  and connection reuse independently checks it. The web precomposed seam aborts
  with `http.ErrAbortHandler` instead of discarding the error.
- Empty `Flush` composes and sends the headers immediately. `FlushError` reports
  the transport error through `http.ResponseController`.
- Exactly 2048 buffered bytes remain pending until completion or overflow.
  Overflow follows bufio's byte-write bypass and string/split-write fill rules.
  Explicit-length responses retain their existing 32 KiB emission coalescing.

`internal/fastserve/wire_regression_test.go` injects prefix-write timeouts followed
by writable deadline clearing, with a pipelined second request. It covers
precomposed, empty, terminal-chunk, flush, interim and body emission failures;
subsequent writes must neither reach the transport nor reuse the connection.
It also requires headers before releasing a blocked empty-flush handler for
HTTP/1.0 and HTTP/1.1, GET and HEAD, and statuses 200, 204 and 304. Raw-wire
comparisons cover 2047/2048/2049 bytes, 1024+1024 and 1000+2000, for byte and
string writes. Single byte-slice writes and recorded response framing are compared
at all three sizes. The web regression requires failed precomposed emission to
abort the handler without fallback output.

Before-fix run (production changes stashed):
`tmp/fastserve-fix/before-final.log`. Precomposed, empty and terminal failures
invoked two handlers and appended a second status line; empty flush timed out
in all twelve cases; 2048 bytes used chunking rather than Content-Length;
1000+2000 emitted a single `bb8` chunk instead of `800` followed by the
remainder. The web seam returned normally rather than aborting.

## Original finding: disconnect cancellation (resolved by follow-up below)

The probe in ignored `tmp/fastserve-fix/disconnect_probe_test.go.txt` was run as
an internal/fastserve test and then removed from the active suite. Results are
in `tmp/fastserve-fix/fastserve-probe.log`: net/http cancels before handler return
for bodyless GET, fully consumed POST, and an already-buffered GET pipeline;
fastserve fails all three (500 ms cancellation wait). No cancellation fix is
included in this branch.

The blocker is read ownership, not context construction. `conn.br` and
`prefixReader.pending` serve both body and subsequent requests, and pending
aliases the reusable request-head buffer. Starting another reader on `br`
would race body reads/drain; starting directly on the socket can steal a body
or pipeline byte. Stopping a blocked detector by read deadline also requires
joining it before restoring deadlines or starting the next request. A probe
that consumes a byte without ordered replay is not a valid fix.

The smallest operational alternative is `CAMPFIRE_SERVER_LOOP=off`, which uses
the existing net/http listener path. A native fix needs a connection-owned
reader with a synchronized one-byte background read, body-EOF notification,
ordered replay behind previously buffered bytes, read-error cancellation,
and explicit abort/join at request completion and handoff. It must cover both
fixed-length and chunked bodies, `Expect: 100-continue`, unread-body drain,
timeouts, and delayed as well as already-buffered pipelines. This reader
lifecycle is deferred rather than partially introduced in the write fix.

## Disconnect follow-up (`fix-disconnect`, base `107c536`)

`disconnectReader` now owns one socket read after body completion. The bodyless
path starts it before the handler; fixed-length completion, chunked EOF, and a
successful body-close drain notify it from the body wrapper. It does not touch
the buffered reader or the aliased head prefix. A byte is replayed after those
older buffered bytes and before any subsequent socket read. EOF/transport errors
cancel the context; successful pipeline data does not. Completion interrupts a
blocked read using a deadline, joins it, and clears the deadline before reuse.
The intentional timeout does not cancel the context.

One worker and its channels are allocated lazily per connection and reused.
No observer read survives handler completion; the parked worker exits and is
joined before the connection is unregistered, including handoff. Body framing
and notification share one allocation: compiler escape output confirms the
inlined `http1.NewBodyReader` temporary does not escape. No new per-request
worker/channel allocation is needed. Sequential requests now need two read
deadline calls for abort/join, plus clearing any body deadline before observing.
Throughput and allocation counts have not been benchmarked.
The socket implementation can allocate its timeout error when an observer is
interrupted; this is not a zero-allocation claim for the full abort path.

Like net/http's one-byte design, observation stops after a successful pipeline
byte until it is consumed. A handler leaving its body unread does not get a
concurrent socket observer; transport errors encountered while reading its body
still cancel its context. This is not a promise to detect every disconnect at
every point in a pipelined stream.

`internal/fastserve/disconnect_test.go` retains the original three probes and adds
forced observer-byte replay with complete response parsing, bounded abort/join
and worker-exit checks, exact-length reads without a final EOF read, close/drain,
and chunked completion. `tmp/disconnect/before.log` records all three fastserve
probes failing while the net/http controls pass; `after.log` records all passing
under the race detector. Follow-up suite logs are in `tmp/disconnect/`.

Follow-up gates passed: full `go test -race -tags sqlite_fts5 -timeout 1200s
-count=1 ./...` under a 1500-second outer timeout (`race-final.log`, fastserve
3.535s, web 14.933s); nested websocket race suite (9.718s); `timeout 300 go vet
./...` (empty `vet-final.log`); `timeout 30 gofmt -l .` (empty output).

## Original wire-fix verification

Raw logs live in ignored `tmp/fastserve-fix/`. No benchmark or `bin/check` run;
performance impact is unmeasured. Final gates passed:

- `timeout 1500 go test -race -tags sqlite_fts5 -timeout 1200s -count=1 ./...`
  (`race-final.log`; fastserve 3.536s, web 15.537s).
- In `third_party/websocket`:
  `timeout 300 go test -race -timeout 240s -count=1 ./...`
  (`websocket-race.log`; websocket 9.381s).
- `timeout 300 go vet ./...` (empty `vet.log`).
- `timeout 30 gofmt -l .` (empty output).

An initial full-suite/vet invocation discovered the diagnostic probe's copied
`.go` file under `tmp/`; it could not compile as a standalone package. The
saved probe was renamed `.go.txt`, and both gates were rerun successfully.
No production files in other worktrees or in `reference/` were changed.
