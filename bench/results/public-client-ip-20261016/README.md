# Public listener: disabled forwarding and Client-IP — 2026-10-16

An independent source audit identified a public-input policy gap. With forwarding
disabled, the front removed the other headers consumed by application normalization,
but retained `Client-IP`. For a trusted/private-range peer, a list such as
`127.0.0.1, 198.51.100.20` passes the application's spoof check against the front's
loopback `X-Forwarded-For`, then supplies the first untrusted IP. Changing the supplied
public address changes the login-throttle identity. The same normalized identity is
used for bans and recorded session IPs.

This particular bypass requires a private/trusted-range peer, such as a LAN connection
or an intermediary that preserves the header. A directly connected globally routable
peer is selected first and prevents this specific bypass. The application listener
intentionally consumes trusted forwarding metadata; the repair belongs at public
intake, not in a tolerant or contradictory application parser.

The public front now discards `Client-IP` when `ForwardHeaders` is false. Explicitly
enabled forwarding is unchanged. There is no schema, cookie format, limiter budget,
proxy allowlist, queue or cryptographic change; this is not a performance optimization.

Verification:

- A permanent regression starts the real public listener, sends ten rejected logins,
  observes throttling on the eleventh, then changes only the spoofed public address.
  Old code returns **401** on request twelve instead of **429**. The fixed code stays
  throttled. This does not hit the application-only target listener.
- The existing consumed-header test covers both stripping and explicitly enabled
  forwarding. The original request remains unmodified.
- A temporary race-enabled probe composes the real TLS/HTTP2 public listener with the
  actual application, using the reference's test CA/certificate cache and normal
  certificate verification. It checks HTTPS redirects despite forged scheme/host
  headers, cross-origin login rejection, successful same-origin login, Secure/
  HttpOnly/SameSite authentication cookies, and the persisted session's loopback IP
  rather than the forged public address. No ACME request or outside service is used.
- Focused race tests and `bin/check` pass. The existing shared authentication workflow
  passes on Go and Rust (**2/2**); it checks ordinary behavior, not this deliberate
  public-header hardening. No bug-for-bug reference requirement is inferred for the
  bypass. The explicit disabled-forwarding policy is the governing contract.

Baseline local `db8e011` has the runtime of PR `41a8c2b`. Compact proof and temporary
TLS probe source are retained here. Full runtime logs, generated databases/storage,
authentication material and binaries stay private. The source audit executed no tests;
parent-owned runs establish the reproduction. This bounded pass found no additional
concrete Host/scheme/origin/cookie issue, not proof of universal proxy correctness.
The separate browser-count diagnostic is retained with the earlier bare-parameter
verification; no production rendering workaround or new browser acceptance claim is
part of this fix.
