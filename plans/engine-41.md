# ENGINE-41 — owned server loop (fastserve)

Status: implemented behind `CAMPFIRE_SERVER_LOOP=on`, default **off** until
measured. Branch `engine-41`.

## Scope

The internal (target) listener only. The public front (TLS/ACME/HTTP2/
compression/cache) stays on net/http. With the flag off, `front.Serve` runs
exactly the listener net/http served before; the flag selects
`internal/fastserve.Serve` for the target listener with the same handler, the
same timing envelope (`ReadTimeout`/`ReadHeaderTimeout`/`WriteTimeout`/
`IdleTimeout`/`MaxHeaderBytes`, `MaxRequestBody` as the reader cap) and the
same protocol configuration for its handoff server.

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
- Concurrency is exercised by `-race` tests, not by a soak.
- The benchmark below is a transport-only microbenchmark (one keep-alive
  connection, no application work); production request cost is dominated by
  the application, so the adoption decision needs interleaved A/B on the
  real workload per the ENGINE-41 plan gate.
- The h2c handoff path is tested with the stdlib client's prior-knowledge
  mode; the internal listener's production configuration has h2c off.

## Measurement (this machine, connected bench, 3 runs, GOMAXPROCS=4, quiet)

    BenchmarkServeNetHTTPSingleConn-4    ~8.2-8.8 µs/op   3331 B/op   30 allocs/op
    BenchmarkServeFastSingleConn-4       ~6.2-6.4 µs/op   3877 B/op   34 allocs/op

The loop is ~25% faster per exchange on this shape with a small allocation
tax (+4/request): the `http.Header` bridge (map, value strings) is
net/http-parity, and the remaining extras are the writev iovec and the
adapter's string conversions. The head parse itself is 0 allocs (simdhttp);
the per-request adapter costs are the reason the loop is not allocation-free
end to end. Recorded before the ENGINE-41 interleaved-A/B adoption gate.