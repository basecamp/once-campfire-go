# Fixed-rate HTTP latency

Build with `go build -o httprate ./bench/httprate`, then supply the base URL and an authenticated cookie. For example:

```sh
./httprate --base http://127.0.0.1:3000 --cookie "$COOKIE" \
  --path /users/me/sidebar --rate 3000 --duration 8 --pacing active
```

The default `--pacing timer` uses runtime timers. Sub-millisecond deadlines can coalesce into bursts. `--pacing active` spins until each deadline and yields after enqueueing so workers can send requests. It uses roughly one generator CPU in addition to request workers; allocate client CPUs separately from the server. The output records the selected mode. Do not mix the two modes in a comparison or treat a pacing improvement as an application improvement.

Arrivals follow absolute deadlines independently of response completion. The worker count and queue are bounded; drops, expired work and unscheduled arrivals remain in the accounting. Scheduled latency includes generator lateness and queueing; service latency begins when a worker takes a request. Neither measures server-only processing time. Precise enqueue times alone do not prove precise network arrivals.

GET mode drains complete encoded bodies without retaining them and checks transport errors, status and nonempty successful bodies. POST mode additionally decodes gzip, verifies its integrity, and checks complete Turbo receipts, content type, request sequence, client identity and a positive message ID before counting success. It emits `[sequence, message_id]` acknowledgements and their client-ID prefix; `bench/application` matches them to exact persisted body/client/creator/room/FTS values.

Latency ends at complete wire-body consumption, before receipt validation. POST validation adds generator work, and acknowledgement retention grows with the completed population; match client binaries between comparison arms rather than assuming this overhead neutral. Use the shared Campfire contracts for broader HTTP and browser behavior. `--help` lists POST, timeout and drain options.
