# ENGINE-41 — owned server loop (fastserve)

Status: implemented; **default on** for the internal listener since
ENGINE-53 (this branch, commit `fastserve: close adapter gaps and
evidence-based default`) and for the plain-HTTP public listener since
ENGINE-62 (`front: owned loop and precomposed assets on the public
listener`): with TLS unconfigured (`DISABLE_SSL`, the benchmark
configuration) the public listener is served by the loop too, with the
recorded-replay lane (below) for the cached auxiliary routes. TLS/ACME
listeners are never affected. `CAMPFIRE_SERVER_LOOP=off` rolls both
listeners back to net/http.

## Scope

The internal (target) listener and, since ENGINE-62, the plain-HTTP public
listener. The public front (TLS/ACME/HTTP2/compression/cache) stays on
net/http when TLS is configured; with the flag off, `front.Serve` runs
exactly the listeners net/http served before. The flag selects
`internal/fastserve.Serve` with the same handler, the same timing envelope
(`ReadTimeout`/`ReadHeaderTimeout`/`WriteTimeout`/`IdleTimeout`/
`MaxHeaderBytes`, `MaxRequestBody` as the reader cap) and the same protocol
configuration for its handoff server. The public chain additionally brings:
- `forward` header edits (in place on the fresh per-request map),
- `PublicCompression` (header policy plus, for pre-encoded bodies, no-op),
- the response cache, whose **recorded-replay lane** (ENGINE-62) emits cached
  hits and the fixed `/up` table from per-entry precomposed head blocks
  (fastserve.AppendHeaderLines) through `response.WriteRecorded`: no header
  map, no sort, one writev, byte-identical to the map path (pinned by
  `internal/front/recorded_test.go` across gzip/identity/zstd/absent, HEAD,
  304, Range and keep-alive streams). The lane only engages for HTTP/1.1
  GET/HEAD without request bodies against entries with reproducible heads
  (no Date/Content-Length/Transfer-Encoding/Trailer/Connection captured, a
  Content-Type whenever the body is non-empty); everything else, including
  captures whose body the public policy encoded (the CE delta tell) and
  HTTP/1.0, takes the byte-identical map path.
- The front capture wrapper (`recordResponse`) deliberately does **not**
  implement Unwrap, so the web layer's precomposed-receiver walk cannot
  bypass the front cache's `X-Cache` bookkeeping on the public listener; the
  internal listener (no wrapper) is unaffected.
- The loop's documented strictness deltas (bare-LF, non-token names,
  duplicate CL/TE, 431 verdicts from the Compatible profile) now apply to
  the public listener as well; they are the same deltas already documented
  below for the internal listener.

## Files

- `internal/fastserve/server.go` — accept loop, handler invocation with
  net/http's recover semantics, shutdown/close lifecycle, handoff server.
- `internal/fastserve/conn.go` — per-connection keep-alive loop: persistent
  buffered reader over a prefix reader (pipelined bytes stay in the loop),
  head parse with `simdhttp/http1.Parse` (Compatible profile), the
  net/http deadline phases (idle → header → body → write), the post-POST
  leading-CRLF tolerance, rejections with net/http's verbatim bytes.
- `internal/fastserve/request.go` — simdhttp head → `*http.Request`, field
  for field with `net/http.readRequest` (Host removed from the map, a valid
  single Content-Length kept, Transfer-Encoding moved to the field,
  Pragma→Cache-Control fold, absolute/CONNECT forms, body framing from
  `http1.FramingOf`, body served by `http1.BodyReader` over the persistent
  reader).
- `internal/fastserve/response.go` — net/http's response semantics on a
  segment writer: the 2048-byte auto-Content-Length budget, chunked
  fallback, sorted header block with per-value lines and sanitization,
  extraHeader order (Date, Content-Length, Content-Type, Connection,
  Transfer-Encoding), suppressed headers for 204/304/1xx, the 100-continue
  rule, the ≤256 KiB post-handler body drain reused by the connection, and
  the keep-alive verdict (close flag, CL underrun, write errors).
- `internal/fastserve/upgrade.go` — `prefixReplayConn` (bytes already
  consumed, replayed before the wire) and the queue listener the internal
  `net/http.Server` Accepts from.
- `internal/fastserve/http.go` — token/close/keep-alive helpers and the
  OPTIONS-asterisk interception.
- `internal/fastserve/parity_test.go` — the differential corpus (below).
- `internal/fastserve/upgrade_test.go` — real WebSocket roundtrip through
  the loop (coder/websocket), h2c through the handoff, preface-with-no-h2
  parity, fragmented preface.
- `internal/fastserve/conn_test.go` — timeouts, concurrency (`-race`),
  panics, 413 + drain + keep-alive reuse, shutdown.
- `internal/fastserve/bench_test.go` — the loop-vs-net/http serving bench.
- `internal/fastserve/ab_bench_test.go` — the ENGINE-53 in-app A/B: the
  application request set (room page, sidebar, search, post) through both
  loops on the same handler.
- `internal/fastserve/request.go` — ENGINE-62: `canonicalHeaderKey` fast path
  for the common request header names (textproto fallback for the rest) and
  `conn.go` drops the dead idle-deadline clear (the header deadline replaces
  it before anything reads; one fewer syscall per keep-alive request).
- `internal/fastserve/response.go` — `AppendHeaderLines` (the composeHead
  line pass exported for the front's recorded head blocks) and
  `WriteRecorded` (a fully recorded response: precomposed head + body, one
  writev, the map path's auto-Content-Length/chunked/close decisions).
- `internal/front/recorded.go` — the recorded-replay lane (above);
  `internal/front/recorded_test.go` — wire parity across the negotiation
  shapes and the engagement pins; `internal/front/loop_bench_test.go` — the
  public-loop end-to-end benchmarks. `recordResponse` no longer implements
  Unwrap (the capture participates in the response, so downstream
  precomposed walks must stop at it).
- `internal/fastserve/soak_test.go` — concurrent keep-alive soak with
  open-fd stability checks (/proc/self/fd before, during and after).
- `internal/front/config.go`, `internal/front/server.go` — the flag and the
  wiring; `internal/front/server_loop_test.go` — flag-off vs flag-on wire
  parity through the real `front.Serve` composition (Deflate, bodyLimit).

## How the loop works

One goroutine per connection (like net/http). Requests on a keep-alive
connection reuse: a persistent `bufio.Reader` over a prefix reader, the head
buffer, the parse state (`http1.Request`), the response body buffer, the head
scratch, the segment list, the header-sort scratch, the remote-address
string. The response is emitted as segments — one contiguous head block plus
body slices — and flushed with one `net.Buffers` writev per flush; fixed-
length bodies coalesce into 32 KiB segments, chunked ones follow the 2048
byte net/http budget. The only per-request heap costs are the ones the
`http.Handler` contract requires (the `http.Header` map, the `*http.Request`,
the value strings) — the same costs net/http pays, plus a fixed adapter tax
(see Measurement).

Upgrade heads (any `Upgrade` header) and connections that open with the
HTTP/2 preface are handed, before the handler runs, to an internal
`net/http.Server` (same handler, same timeouts, the replaced listener's
`Protocols`) via a `net.Conn` wrapper that replays every byte the loop
consumed: the parsed head, the over-read body bytes, and the buffered
reader's contents. WebCable's WebSocket accept and h2c keep working
unchanged because they never run inside this package. The loop never sees a
hijack; handed-off conns are owned by the handoff server (the loop's
deferred close skips them).

## Differential corpus

`TestParityCorpus` sends identical raw bytes to a stock net/http server and
the loop (same handler, same limits) and compares the full wire bytes after
masking `Date`; chunked responses are compared with framing normalized
(status, headers, decoded body — chunk boundaries are a function of write
shape, and net/http's own depend on it). Covered: basic/HEAD/empty/204/304/
redirect/404/cookies/413 (via `http.Error`), fixed and chunked request
bodies, unread-body drain (small and >256 KiB), 100-continue (read and
unread), 417, OPTIONS asterisk, HTTP/1.0 with and without keep-alive,
Connection: close, folded headers, Pragma fold, the 431 cap at 4 KiB, and
the malformed corpus (garbage, missing Host with net/http's exact text,
missing colon, duplicate differing Content-Lengths, absolute-form target,
control bytes, bad escapes, malformed Content-Length). `TestParityKeepAliveReuse`
covers a multi-request sequence with the post-POST CRLF tolerance;
`TestParityPipelined` back-to-back requests in one write; `TestParityUpgradeNotHijacked`
an Upgrade-headed request the handler does not upgrade. The front-level
`TestServerLoopFlagDiff` runs the real `front.Serve` composition (Deflate,
bodyLimit) flag-off vs flag-on with the same corpus plus gzip/406/keep-alive.

## Deliberate differences (all inherited from the simdhttp head parser)

These are pinned by `TestParityDeltas` so a simdhttp bump cannot silently
change them. Deliberately stricter than net/http (anti-smuggling choices of
the parser's documented contract):

- bare-LF line endings: 400 here, accepted by net/http
- header names must be RFC 9110 tokens (a space in a name): 400 here, 400
  with different text in net/http ("invalid header name")
- duplicate Content-Length (even equal) / duplicate Transfer-Encoding:
  400 here; net/http dedupes equal Content-Lengths and answers 501 for
  multiple transfer-encodings
- transfer encoding other than exactly "chunked": 400 here, 501 in
  net/http (RFC 9110); Transfer-Encoding alongside Content-Length:
  400 here, accepted (CL deleted) by net/http
- HTTP versions other than /1.0 and /1.1: 400 here, 505 in net/http
- empty Host counts as missing: 400 here, accepted by net/http
- limit verdicts: the Compatible profile caps the request line at 8 KiB and
  100 headers; an over-long line or many headers answer 431 here where
  net/http would still accept (its 64 KiB head cap on this listener)

## Verification limits

- Malformed-head cases assert status parity except where a delta is
  documented above; net/http's per-case texts are not all replicated.
- Chunked response boundaries are not byte-compared (decoded comparison);
  fixed-length responses are byte-exact.
- Concurrency is exercised by `-race` tests plus the fd-stability soak
  (`TestSoakConcurrentKeepAlive`): 24 keep-alive clients x 150 mixed
  requests with mid-soak and post-soak open-fd checks.
- The benchmark below is a transport-only microbenchmark (one keep-alive
  connection, no application work); the adoption decision (ENGINE-53) used
  the interleaved A/B on application-shaped requests, below.
- The h2c handoff path is tested with the stdlib client's prior-knowledge
  mode; the internal listener's production configuration has h2c off.

## ENGINE-53 measurement (this machine, GOMAXPROCS=4, quiet, CPUs 16-31)

Transport microbench (one keep-alive connection, fixed 418-byte response),
interleaved medians of 3 runs each:

    BenchmarkServeNetHTTPSingleConn-4    ~7.93-8.05 µs/op   3331 B/op   30 allocs/op
    BenchmarkServeFastSingleConn-4       ~6.16-6.29 µs/op   3125 B/op   29 allocs/op

The loop is ~23% faster with one fewer allocation and fewer bytes; the
adapter tax recorded below is closed (ENGINE-53): the header-map bridge,
the response head block and the body accumulation all reuse per-connection
scratch (origin slices kept through the flush window), the Proto string
comes from the parser's two accepted constants, WriteString appends in
place, the drain scratch lives on the connection, and net.Buffers.WriteTo
runs on the connection's own list so the writev dispatch allocates nothing.

In-app A/B (same handler on both loops, interleaved min-of-7 medians,
benchstat): recorded-piece room page (identity, 374 KiB in 4 parts),
sidebar fragment, search results, form POST answering 302:

    route     net/http     fastserve    speedup   allocs (net/http -> fast)
    room      153.2 µs     48.2 µs      3.18x     29 -> 29
    sidebar   15.20 µs     8.73 µs      1.74x     49 -> 32
    search    10.92 µs     8.34 µs      1.31x     37 -> 32
    post      8.42 µs      6.41 µs      1.31x     33 -> 35 (+2: per-request context)

perf stat (room, per op): fastserve 67k cycles / 129k instructions vs
net/http 243k / 446k; sidebar 15.8k / 38.5k vs 40.9k / 77.8k.

Adoption verdict: default on. The differential corpus (above), the
websocket/h2c handoff, keep-alive/pipelined reuse, malformed input and
timeout suites are green under `-race`, the front-level
`TestServerLoopFlagDiff` byte-compares the full `front.Serve` composition
flag-on vs flag-off, and requests through the loop carry a per-request
cancellable context like net/http's. Rollback: `CAMPFIRE_SERVER_LOOP=off`.

ENGINE-53 also closed parity gaps found in this review: the response write
deadline is cleared after each response (net/http parity, so a keep-alive
connection outliving a short WriteTimeout is not cut by the stale
deadline); the ReadTimeout budget is anchored at the head read start (head
and body share one budget, as in net/http); the automatic Content-Length
now respects a handler-set Transfer-Encoding (net/http's !hasTE gate);
http.MaxBytesReader overflow marks the response requestTooLarge — the
unexported-method interface net/http uses cannot cross the package
boundary, so the loop detects the overflow at the post-handler drain and
places Connection: close exactly where net/http's bytes put it; and
responses served while Shutdown is in progress carry Connection: close
(net/http's doKeepAlives gate).

Additional deltas from ENGINE-53, narrower than the parse deltas above:

- Request contexts are cancelled when the request completes (net/http
  parity) but not on a client disconnect mid-request: net/http detects
  that with a per-request background-read goroutine, which this loop does
  not spawn (the write path still fails fast on a dead peer).
- Response trailers (net/http's `Trailer:` pseudo-headers) are not
  supported.
- A handler that closes the request body early: net/http closes the
  connection (unread bytes undeclared), this loop drains up to the
  post-handler budget and can reuse it — a strictly wider keep-alive.
- Requests whose head is already parsed when Shutdown starts are served
  (net/http drops them); the response still carries Connection: close.
- A body that trips http.MaxBytesReader after the handler already wrote
  its header places Connection: close at the end of the header block
  where net/http would interleave it inline; status, values and close all
  match.