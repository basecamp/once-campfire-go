# Go optimization investigation

See the [final before/after comparison](comparison.md) and
[complete alternating benchmark](../application-optimized-20261003/report.md).

The starting point is the full port measured in
[the original application benchmark](../application-final-20261002/report.md).
These profiles and short trials guided implementation; use the final alternating
application benchmark for comparative results, not the fastest exploratory sample.

Changes:

- Derive Rails signed-ID and signed-GID keys once when initializing Secrets.
  The sidebar profile originally spent about 80% of CPU in PBKDF2 because each
  avatar URL derived the same key again. Golden signing vectors remain unchanged.
- Cache at most 256 prepared read statements, and fetch sidebar membership flags
  in the room query instead of issuing a query per room. Statements do not cache
  query results or authorization decisions.
- Reuse bounded response buffers. Cache complete versioned message lists and their
  hashes; write large immutable byte slices directly rather than copying them
  through the template and response buffers. An intermediate string-based approach
  was slower because it generated many small socket writes; `parts/` replaces it.
- Query message IDs and update timestamps for the common history-page path, loading
  full message records only on fragment misses. Cache sidebar HTML using every
  rendered room/user/permission value after fresh database reads.
- Serialize and compress broadcasts once per subscription identifier. The local
  coder/websocket extension uses its existing frame writer and adds immutable
  prepared messages. Bounded outgoing queues hold 256 frames. Each publication
  checks current sessions, user status and room memberships in batches, including
  session-to-user identity. No authorization result survives a publication.
- Generate stored plain text without rendering five unused rich-text variants.
  The focused path is compared with the full processor throughout the golden corpus.
- Use the same fixed dummy bcrypt digest as Rust for nonexistent-user logins,
  avoiding a cost-12 hash generation at every process startup. Login verification
  still performs the bcrypt comparison.

`bench/profile` records 10-second CPU profiles using the real seed and original
Rust load generator, four application CPUs (8–11), and load generator CPUs 12–15.
The directories record successive stages (`before`, `keys`, `recorded`, `parts`,
`references`, `sidebar-cache`). They are single-workload diagnostic samples, not
repeated comparative measurements. Profile CPU includes startup and login.

`step2/` is an intentionally retained failed experiment: compressed Cable delivery
was incomplete when publication became faster than per-client compression. Its
partial results must not be presented as a valid benchmark. `step3/` passed all
six Cable configurations in a short trial after shared compression was introduced;
it predates the final HTTP and authorization improvements.
