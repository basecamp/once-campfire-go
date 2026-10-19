# C port teardown (2026-10-14)

Read from `basecamp/once-campfire-c` (clone at
`/home/sebishogun/Work/Development/once-campfire-c`) against our engine, after
the published table moved to C 137,524-152,002 rps on the read routes. Findings
with file:line evidence; adoption notes for this branch.

## What C does that explains its read rates

1. **Complete-body cache with a version-mutex admission rule.**
   `src/cache.h`: one process-wide table, 4096 buckets, separate chaining, one
   mutex shared with version advancement, FIFO eviction, 1 MiB entry cap,
   HMAC-SHA256 (64-bit truncated) keys. `cf_cache_get` serves a body only while
   its stored version equals the current data version; `cf_cache_put(key,
   expected_version, body)` admits only if `expected_version` is still current
   **under the same mutex** that advances the version, so no check-then-insert
   race exists (this is the mid-read admission property DHH asked for). Budget
   `CF_CACHE_BYTES` defaults to 64 MiB; 0 disables with no diagnostic.
2. **Two version sources.** An in-process atomic `data_version` advanced by
   local commits (`src/app.c:261-289`) plus a dedicated reader that runs
   `PRAGMA data_version` and compares against `observed_data_version`
   (`src/db/reader.c:519-529`) for foreign commits. The cache borrows the
   atomic through `cf_app_cache_version`.
3. **HTTP: level-triggered epoll loops, count follows CPU affinity, capped at
   four** (`CF_LOOPS` override, `CF_CONNECTIONS_PER_LOOP`) — `src/http/loop.c`
   — plus a request pool for application work with admission accounting
   (`cf_app_request_stop` wakes the pool). HTTP/2 in `src/front/h2.c`, TLS in
   `src/front/tls.c`.
4. **SQLite**: WAL, `synchronous=NORMAL`, `foreign_keys=ON`, `mmap_size=0`,
   `query_only=ON` on readers, a dedicated writer connection, prepared
   statements (`src/db/statements.c`).
5. **Compression**: vendored zlib-ng; cached bodies store the gzip member and
   ETag alongside the plain bytes.
6. **Rendering**: `src/presenters` + `src/richtext` (vendored gumbo for HTML
   parsing), not a compiled template interpreter.

## Where we already match

- Version-gated admission: our response cache, search cache, sidebar gate and
  cable authorization memo now all carry the observed generation
  (`PRAGMA data_version` on a pinned reader). The HTTP and publish paths read
  it through a direct cgo handle (`internal/fastdb/csqlite/version.go`), so a
  read allocates nothing — the same cost model as C's reader.
- Whole-response caching for room/messages (`CAMPFIRE_RESPONSE_CACHE_MB`,
  64 MiB) and whole-page sidebar caching, with byte-identical bodies and
  authorization rechecks.
- Owned HTTP loop with precomposed heads and one writev per response;
  cached gzip members stored with their ETag.

## Candidate next steps (measured gap: we are 73-83% of C on reads)

- The remaining per-hit work on our side: the session/user read for
  authorization, the generation read, and the socket write. C pays the same
  shape; the difference may be scheduling (epoll loops vs Go netpoll with
  per-connection goroutines) and its 4096-bucket mutex table vs our per-cache
  maps.
- Profile a warm room hit end to end on the shared harness and compare the
  syscall profile with C's loop (`src/http/output.c`) before changing
  anything; do not port the event-loop model without evidence, because Go's
  netpoll already batches epoll waits.
